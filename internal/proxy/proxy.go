// Package proxy is the base-URL proxy: applications point OPENAI_BASE_URL,
// ANTHROPIC_BASE_URL and friends at http://127.0.0.1:4141/<provider>/...
// and every call is recorded from the provider's own usage block after it
// has streamed to the client. Bytes pass through unbuffered; bodies are
// never written anywhere. Recording is best-effort, proxying is not.
package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
)

const (
	reqHeadBytes   = 4 << 10
	reqRewriteMax  = 16 << 20
	tailBytes      = 64 << 10
	AdapterVersion = "0.2.0"
	callIDHeader   = "X-Tokenpylon-Call-Id"
	attemptHeader  = "X-Tokenpylon-Attempt"
	gatewayHeader  = "X-Tokenpylon-Gateway"
)

var hopByHop = map[string]bool{"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true, "te": true, "trailer": true, "trailers": true, "transfer-encoding": true, "upgrade": true, "host": true}

// Sink receives finished events. It must not block the data path.
type Sink func(*event.Event)

type Options struct {
	InstallID    string
	AllowPrivate bool // /proxy/127.0.0.1:port upstreams (LiteLLM, Ollama)
	AnyHost      bool // skip the loopback Host check (container sidecar bound to 0.0.0.0)
	Sink         Sink
	Log          func(string)
}

type Handler struct {
	opts      Options
	transport *http.Transport // named upstreams
	generic   *http.Transport // /proxy/<host>: resolved addresses are checked before dialing
	insecure  *http.Transport // /proxy/<loopback>: plain HTTP
	mu        sync.Mutex
	inflight  int
	total     int64
	sinks     sync.WaitGroup
}

// checkedDial resolves the name itself and refuses private, loopback,
// link-local and unspecified addresses unless allowPrivate is set, then
// dials the vetted address directly: a DNS name cannot smuggle the
// request to something the host-literal check would have refused.
func checkedDial(allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ip := range ips {
			if !allowPrivate && (ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast()) {
				last = fmt.Errorf("refusing to connect to a private address for %s", host)
				continue
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("no address for %s", host)
		}
		return nil, last
	}
}

func New(opts Options) *Handler {
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
		DisableCompression:    true, // usage must be readable off the wire
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	ins := t.Clone()
	ins.Proxy = nil
	gen := t.Clone()
	gen.Proxy = nil // an environment proxy would bypass the address check
	gen.DialContext = checkedDial(opts.AllowPrivate)
	return &Handler{opts: opts, transport: t, generic: gen, insecure: ins}
}

// Wait blocks until every detached recording goroutine has delivered.
func (h *Handler) Wait() { h.sinks.Wait() }

func (h *Handler) Inflight() int { h.mu.Lock(); defer h.mu.Unlock(); return h.inflight }
func (h *Handler) Total() int64  { h.mu.Lock(); defer h.mu.Unlock(); return h.total }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) log(s string) {
	if h.opts.Log != nil {
		h.opts.Log(s)
	}
}

// ServeHTTP handles provider-prefixed paths. Anything else is 404.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	// Only the loopback name may address this listener: a browser tricked
	// by DNS rebinding sends another Host and gets nothing.
	if !h.opts.AnyHost && host != "" && !isLoopback(strings.Trim(host, "[]")) {
		writeJSON(w, 403, map[string]any{"error": "loopback only"})
		return
	}
	path := r.URL.Path
	route, ok := Resolve(path, h.opts.AllowPrivate)
	if !ok {
		names := make([]string, 0, len(Upstreams))
		for k := range Upstreams {
			names = append(names, k)
		}
		writeJSON(w, 404, map[string]any{"error": "unknown provider path", "providers": names, "generic": "/proxy/<api-host>/..."})
		return
	}
	h.mu.Lock()
	h.inflight++
	h.total++
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.inflight--; h.mu.Unlock() }()
	h.proxy(w, r, route)
}

func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, rt Route) {
	started := time.Now()
	callID := r.Header.Get(callIDHeader)
	attempt, _ := strconv.Atoi(r.Header.Get(attemptHeader))
	gatewayHint := r.Header.Get(gatewayHeader)

	// Request headers: everything the client sent minus hop-by-hop and our
	// own. Authorization passes through untouched and is never logged.
	upURL := "https://"
	if rt.Upstream.Insecure {
		upURL = "http://"
	}
	hostport := rt.Upstream.Host
	if rt.Upstream.Port != 0 {
		hostport += ":" + strconv.Itoa(rt.Upstream.Port)
	}
	upURL += hostport + rt.Upstream.BasePath + rt.Rest
	if r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}

	var body io.Reader = r.Body
	var head []byte
	var pr *peekReader
	contentLength := r.ContentLength
	op, mode := operationOf(rt.Rest)
	rewrite := rt.Upstream.Style == StyleOpenAI && op == "chat" && strings.HasSuffix(strings.TrimSuffix(rt.Rest, "/"), "/chat/completions") && r.Method == http.MethodPost && contentLength <= reqRewriteMax
	if rewrite {
		// Bounded read for the include_usage rewrite. Larger than the bound:
		// forward what was read, then stream the rest.
		lr := io.LimitReader(r.Body, reqRewriteMax+1)
		buf, err := io.ReadAll(lr)
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": "bad request body"})
			return
		}
		if len(buf) > reqRewriteMax {
			body = io.MultiReader(bytes.NewReader(buf), r.Body)
			contentLength = -1
		} else {
			out := WithIncludeUsage(buf)
			body = bytes.NewReader(out)
			contentLength = int64(len(out))
		}
		head = buf[:min(len(buf), reqHeadBytes)]
	} else if r.Method == http.MethodPost || r.Method == http.MethodPut {
		// Peek the head for the model without holding the body.
		pr = &peekReader{r: r.Body, max: reqHeadBytes}
		body = pr
	}

	ctx := r.Context()
	upReq, err := http.NewRequestWithContext(ctx, r.Method, upURL, body)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "bad upstream"})
		return
	}
	upReq.ContentLength = contentLength
	nominated := map[string]bool{}
	for _, c := range strings.Split(r.Header.Get("Connection"), ",") {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			nominated[c] = true
		}
	}
	for k, vs := range r.Header {
		lk := strings.ToLower(k)
		if hopByHop[lk] || nominated[lk] || strings.HasPrefix(lk, "x-tokenpylon-") {
			continue
		}
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}
	upReq.Header.Set("Accept-Encoding", "identity")
	upReq.Host = hostport

	tr := h.transport
	if rt.Upstream.Insecure {
		tr = h.insecure
	} else if rt.Generic {
		tr = h.generic
	}
	res, err := tr.RoundTrip(upReq)
	if err != nil {
		cancelled := ctx.Err() != nil
		if !cancelled {
			writeJSON(w, 502, map[string]any{"error": "upstream unreachable"})
		}
		h.record(rt, started, nil, nil, head, pr, nil, cancelled, false, op, mode, callID, attempt, gatewayHint, false)
		return
	}
	defer res.Body.Close()

	for k, vs := range res.Header {
		lk := strings.ToLower(k)
		if hopByHop[lk] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)
	isStream := strings.Contains(res.Header.Get("Content-Type"), "text/event-stream")
	// A body we cannot read as sent (compressed despite Accept-Encoding:
	// identity, or not JSON/SSE at all) yields no usage: unknown, never a
	// number scraped out of compressed bytes.
	enc := strings.ToLower(res.Header.Get("Content-Encoding"))
	ctype := strings.ToLower(res.Header.Get("Content-Type"))
	readable := (enc == "" || enc == "identity") && (isStream || strings.HasPrefix(ctype, "application/json"))
	// Recording runs off the data path: chunks are copied into a bounded
	// queue and parsed by their own goroutine; a full queue drops for
	// recording only, never for the client.
	var su *StreamUsage
	var bu *BodyUsage
	var rec *recorder
	switch {
	case !readable:
		rec = newRecorder(func([]byte) {})
	case isStream:
		su = NewStreamUsage(rt.Upstream.Style)
		rec = newRecorder(su.Write)
	default:
		bu = NewBodyUsage(rt.Upstream.Style)
		rec = newRecorder(bu.Write)
	}
	var firstByte time.Time
	buf := make([]byte, 32<<10)
	complete := false
	cancelled := false
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if firstByte.IsZero() {
				firstByte = time.Now()
			}
			chunk := buf[:n]
			if _, werr := w.Write(chunk); werr != nil {
				cancelled = true
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			rec.Write(chunk)
		}
		if rerr == io.EOF {
			complete = true
			break
		}
		if rerr != nil {
			cancelled = ctx.Err() != nil
			break
		}
	}
	// Draining is bounded: the client already has every byte. A dropped
	// chunk or a slow parser means unknown usage, never a wrong number.
	lost := !rec.Close(2 * time.Second)
	var u *Usage
	if !lost {
		if su != nil {
			u = su.Result()
		} else if bu != nil {
			u = bu.Result()
		}
	}
	var ttft *time.Time
	if !firstByte.IsZero() {
		ttft = &firstByte
	}
	h.record(rt, started, res, u, head, pr, ttft, cancelled, complete, op, mode, callID, attempt, gatewayHint, isStream)
}

// peekReader keeps the first bytes of a request body while the transport
// consumes it. The transport may still be reading when the response
// arrives, so access is locked and Head returns a copy.
type peekReader struct {
	r    io.Reader
	max  int
	mu   sync.Mutex
	head []byte
}

func (p *peekReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.mu.Lock()
		if len(p.head) < p.max {
			p.head = append(p.head, b[:min(n, p.max-len(p.head))]...)
		}
		p.mu.Unlock()
	}
	return n, err
}

func (p *peekReader) Head() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.head...)
}

var safeHost = regexp.MustCompile(`[^a-z0-9_.-]`)

func (h *Handler) record(rt Route, started time.Time, res *http.Response, u *Usage, head []byte, pr *peekReader, firstByte *time.Time, cancelled, complete bool, op, mode, callID string, attempt int, gatewayHint string, stream bool) {
	defer func() { _ = recover() }()
	if pr != nil && len(head) == 0 {
		head = pr.Head()
	}
	e := &event.Event{
		Schema: event.SchemaVersion, InstallID: h.opts.InstallID,
		Adapter: "proxy-" + string(rt.Upstream.Style), AdapterVersion: AdapterVersion, RecorderVersion: event.RecorderVersion,
		OccurredAt: started.UTC().Format(time.RFC3339Nano), RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Attempt: max(1, attempt), Provider: rt.Provider, Operation: op, Mode: mode,
		Stream: event.B(stream), Cancelled: cancelled, Complete: complete && !cancelled,
		AmountBasis: event.BasisNone, ServedHost: "unknown", HostEvidence: "none",
	}
	_, off := started.Zone()
	e.TZOffsetMin = event.I(off / 60)
	if callID != "" {
		e.LogicalCallID = event.S(callID)
	}
	if Gateways[rt.Provider] {
		e.Gateway = event.S(rt.Provider)
	} else if gatewayHint != "" {
		e.Gateway = event.S(strings.ToLower(gatewayHint))
	}
	// requested model: the request body; returned model: the response.
	var requested *string
	if rt.Upstream.Style == StyleGoogle {
		if m := regexp.MustCompile(`/models/([^:/]+)`).FindStringSubmatch(rt.Rest); m != nil {
			requested = &m[1]
		}
	}
	if requested == nil {
		requested = TopLevelModel(head)
	}
	if u != nil && u.Model != nil {
		e.ReturnedModel = u.Model
	}
	if requested == nil {
		requested = e.ReturnedModel
	}
	if requested == nil {
		return // nothing to price; not worth a row
	}
	e.RequestedModel = *requested
	if u != nil {
		e.InputTokens, e.CachedTokens, e.CacheWriteTokens, e.OutputTokens, e.ReasoningTokens = u.Input, u.Cached, u.CacheWrite, u.Output, u.Reasoning
		e.CacheWrite1hTokens = u.CacheWrite1h
		e.ReasoningInOutput, e.CachedInInput = u.ReasoningInOutput, u.CachedInInput
		e.ProviderRequestID = u.RequestID
		e.ServiceTier = u.ServiceTier
		if u.CostUSD != nil {
			e.CostUSD, e.AmountBasis, e.Currency = u.CostUSD, event.BasisProviderReported, event.S("USD")
		}
	}
	if res != nil {
		e.Status = event.I(res.StatusCode)
		if e.ProviderRequestID == nil {
			for _, k := range []string{"X-Request-Id", "Request-Id", "Cf-Ray"} {
				if v := res.Header.Get(k); v != "" {
					e.ProviderRequestID = event.S(v)
					break
				}
			}
		}
		if v := res.Header.Get("X-Openrouter-Provider"); v != "" {
			e.ServedHost, e.HostEvidence = safeHost.ReplaceAllString(strings.ToLower(v), "_"), "response_header"
		}
	}
	if e.HostEvidence == "none" && u != nil && u.ServedHost != nil {
		e.ServedHost, e.HostEvidence = safeHost.ReplaceAllString(*u.ServedHost, "_"), "response_body"
	}
	if e.HostEvidence == "none" && rt.Upstream.Direct {
		e.ServedHost, e.HostEvidence = rt.Provider, "direct_provider"
	}
	e.LatencyMs = event.I(int(time.Since(started) / time.Millisecond))
	if firstByte != nil {
		e.TTFTMs = event.I(int(firstByte.Sub(started) / time.Millisecond))
	}
	e.StatusClass = event.ClassOf(e.Status, cancelled)
	if err := e.Validate(); err != nil {
		h.log("event rejected: " + err.Error())
		return
	}
	if h.opts.Sink != nil {
		h.sinks.Add(1)
		go func() { defer h.sinks.Done(); h.opts.Sink(e) }()
	}
}

// Serve binds the loopback listener. ctx cancellation shuts it down.
func Serve(ctx context.Context, addr string, mux http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(c) // returns after in-flight handlers finish (or the timeout)
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		<-done // join the shutdown so callers can tear down after handlers
		return nil
	}
	return err
}
