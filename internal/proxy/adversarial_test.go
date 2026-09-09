package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
)

func TestOversizeBodyPassesThroughUntouched(t *testing.T) {
	var got int
	var sawUsageFlag bool
	up, px, events, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = len(b)
		sawUsageFlag = bytes.Contains(b, []byte("include_usage"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"big-1","model":"m","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("x", reqRewriteMax+100) + `"}]}`
	res, err := http.Post(srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body)
	if got != len(body) || sawUsageFlag {
		t.Fatalf("oversize body altered: got %d want %d, rewritten=%v", got, len(body), sawUsageFlag)
	}
	e := wait(t, mu, events)
	if e.RequestedModel != "m" {
		t.Fatalf("model from oversize head: %+v", e)
	}
}

func TestGzipUpstreamPassesThrough(t *testing.T) {
	up, px, events, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		// Ignores Accept-Encoding: identity, as a misbehaving host might.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(`{"id":"gz-1","model":"m","usage":{"prompt_tokens":3,"completion_tokens":1}}`))
		gz.Close()
		_, _ = w.Write(buf.Bytes())
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	tr := &http.Transport{DisableCompression: true}
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("encoding header dropped")
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("client got corrupted gzip: %v", err)
	}
	plain, _ := io.ReadAll(zr)
	if !bytes.Contains(plain, []byte(`"gz-1"`)) {
		t.Fatalf("payload altered: %s", plain)
	}
	e := wait(t, mu, events)
	if e.InputTokens != nil { // gzip is opaque to the extractor: unknown, never wrong
		t.Fatalf("invented usage from gzip: %+v", e)
	}
	if e.RequestedModel != "m" || *e.Status != 200 {
		t.Fatalf("event: %+v", e)
	}
}

func TestUpstreamDiesMidStream(t *testing.T) {
	up, px, events, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: {\"id\":\"d1\",\"model\":\"m\",\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		hj, ok := w.(http.Hijacker)
		if ok {
			c, _, _ := hj.Hijack()
			c.Close() // connection torn down without a terminating chunk
		}
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	res, err := http.Post(srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body) // must not hang
	e := wait(t, mu, events)
	if e.Complete || e.Cancelled {
		t.Fatalf("truncated upstream recorded as complete/cancelled: %+v", e)
	}
}

func TestCRLFAndMultiDataSSE(t *testing.T) {
	s := NewStreamUsage(StyleOpenAI)
	s.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\"}\r\n\r\ndata: {\"id\":\"c1\",\r\ndata: \"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\r\n\r\n"))
	u := s.Result()
	if u == nil || u.Input == nil || *u.Input != 4 {
		t.Fatalf("CRLF/multi-data: %+v", u)
	}
}

func TestConcurrentStreamsWithAborts(t *testing.T) {
	var served int64
	up, px, events, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&served, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		for i := 0; i < 20; i++ {
			_, _ = io.WriteString(w, "data: {\"id\":\"s\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n")
			f.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"s\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":20}}\n\ndata: [DONE]\n\n")
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	const n = 150
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			if rand.Intn(3) == 0 {
				buf := make([]byte, 64)
				_, _ = res.Body.Read(buf)
				cancel()
				return
			}
			_, _ = io.ReadAll(res.Body)
			res.Body.Close()
		}(i)
	}
	wg.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := len(*events)
		mu.Unlock()
		if c == n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*events) != n {
		t.Fatalf("recorded %d of %d requests", len(*events), n)
	}
	var cancelled, complete int
	for _, e := range *events {
		if e.Cancelled {
			cancelled++
		}
		if e.Complete {
			complete++
			if e.OutputTokens == nil || *e.OutputTokens != 20 {
				t.Fatalf("complete stream without usage: %+v", e)
			}
		}
	}
	if cancelled == 0 || complete == 0 || cancelled+complete != n {
		t.Fatalf("cancelled=%d complete=%d", cancelled, complete)
	}
	if px.Inflight() != 0 {
		t.Fatalf("inflight leak: %d", px.Inflight())
	}
}

func TestOddRequests(t *testing.T) {
	up, px, _, _ := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/proxy/" + u.Host + "/v1/models", 200},
		{"HEAD", "/proxy/" + u.Host + "/v1/models", 200},
		{"POST", "/proxy/" + u.Host + "/v1/chat/completions", 200}, // non-JSON body below
		{"GET", "/proxy/..%2f..%2fetc/passwd", 404},
		{"GET", "/proxy/evil.example:99999/v1", 404},
		{"GET", "/openai", 200},
		{"GET", "/", 404}, // the proxy alone: the page is mounted by the collector
	} {
		var body io.Reader
		if tc.method == "POST" {
			body = strings.NewReader("not json at all")
		}
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, body)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_, _ = io.ReadAll(res.Body)
		if tc.path == "/openai" {
			continue // real upstream; only reachability of the route matters and it needs network
		}
		if res.StatusCode != tc.want {
			t.Fatalf("%s %s: %d want %d", tc.method, tc.path, res.StatusCode, tc.want)
		}
	}
	// IPv6 loopback Host is accepted; a spoofed numeric non-loopback is not.
	for host, want := range map[string]int{"[::1]:4141": 200, "10.0.0.5:4141": 403, "127.1.2.3": 200, "localhost:9": 200} {
		req, _ := http.NewRequest("GET", srv.URL+"/proxy/"+u.Host+"/v1/models", nil)
		req.Host = host
		res, _ := http.DefaultClient.Do(req)
		_, _ = io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("Host %s: %d want %d", host, res.StatusCode, want)
		}
	}
	_ = event.SchemaVersion
}

func TestStructuralExtractionIgnoresNestedUsage(t *testing.T) {
	// A usage-shaped object inside content (tool output echoing a JSON
	// document) must not become telemetry; only the top-level one counts.
	body := `{"id":"r1","model":"m","choices":[{"message":{"content":"{\"usage\":{\"prompt_tokens\":999,\"completion_tokens\":999}}","usage":{"prompt_tokens":555}}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`
	bu := NewBodyUsage(StyleOpenAI)
	bu.Write([]byte(body))
	u := bu.Result()
	if u == nil || *u.Input != 7 || *u.Output != 2 || *u.RequestID != "r1" {
		t.Fatalf("bad: %+v", u)
	}
	// nested "model" inside choices must not override the top-level one
	bu = NewBodyUsage(StyleOpenAI)
	bu.Write([]byte(`{"choices":[{"model":"inner","id":"inner"}],"model":"outer","id":"outer","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	u = bu.Result()
	if *u.Model != "outer" || *u.RequestID != "outer" {
		t.Fatalf("nested strings leaked: %+v", u)
	}
	// non-object bodies yield nothing
	bu = NewBodyUsage(StyleOpenAI)
	bu.Write([]byte(`<html>"usage":{"prompt_tokens":3}</html>`))
	if bu.Result() != nil {
		t.Fatal("non-JSON body produced usage")
	}
}

func TestResponsesStreamCompletedNested(t *testing.T) {
	s := NewStreamUsage(StyleOpenAI)
	s.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_9\",\"model\":\"gpt-5\"}}\n\n"))
	s.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"usage\\\":{\\\"input_tokens\\\":99}}\"}\n\n"))
	s.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_9\",\"model\":\"gpt-5\",\"output\":[{\"content\":[{\"text\":\"{\\\"usage\\\":{\\\"input_tokens\\\":77}}\"}]}],\"usage\":{\"input_tokens\":12,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens\":3}}}\n\n"))
	u := s.Result()
	if u == nil || *u.Input != 10 || *u.Cached != 2 || *u.Output != 3 || *u.RequestID != "resp_9" {
		t.Fatalf("bad: %+v", u)
	}
}

func TestNonJSONContentTypeYieldsNoUsage(t *testing.T) {
	up, px, events, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `{"id":"h1","model":"m","usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	res, _ := http.Post(srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	_, _ = io.ReadAll(res.Body)
	e := wait(t, mu, events)
	if e.InputTokens != nil || e.ProviderRequestID != nil {
		t.Fatalf("usage taken from a non-JSON content type: %+v", e)
	}
}

func TestCheckedDialRefusesPrivateNames(t *testing.T) {
	d := checkedDial(false)
	if _, err := d(context.Background(), "tcp", "localhost:1"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("localhost not refused: %v", err)
	}
	if _, err := d(context.Background(), "tcp", "127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("loopback literal not refused: %v", err)
	}
	if _, err := d(context.Background(), "tcp", "10.1.2.3:1"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private literal not refused: %v", err)
	}
}
