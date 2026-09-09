package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
)

// withUpstream routes a fake provider at a loopback address via /proxy/.
func withUpstream(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Handler, *[]*event.Event, *sync.Mutex) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	var mu sync.Mutex
	var got []*event.Event
	px := New(Options{InstallID: "r_0123456789abcdef", AllowPrivate: true, Sink: func(e *event.Event) { mu.Lock(); got = append(got, e); mu.Unlock() }})
	return up, px, &got, &mu
}

func wait(t *testing.T, mu *sync.Mutex, got *[]*event.Event) *event.Event {
	t.Helper()
	for i := 0; i < 100; i++ {
		mu.Lock()
		if len(*got) > 0 {
			e := (*got)[0]
			mu.Unlock()
			return e
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no event")
	return nil
}

func TestProxyJSONAndIncludeUsage(t *testing.T) {
	var seenBody []byte
	var seenAuth string
	up, px, got, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("X-Request-Id", "req_1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"gpt-5-2026","usage":{"prompt_tokens":12,"completion_tokens":4}}`))
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	req.Host = "127.0.0.1"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if !bytes.Contains(body, []byte(`"prompt_tokens":12`)) {
		t.Fatalf("body not passed through: %s", body)
	}
	if !bytes.Contains(seenBody, []byte(`"include_usage":true`)) || seenAuth != "Bearer sk-test" {
		t.Fatalf("upstream saw %s auth=%q", seenBody, seenAuth)
	}
	e := wait(t, mu, got)
	if e.RequestedModel != "gpt-5" || *e.ReturnedModel != "gpt-5-2026" || *e.InputTokens != 12 || *e.OutputTokens != 4 || *e.ProviderRequestID != "chatcmpl-1" || *e.Status != 200 || !e.Complete || e.Adapter != "proxy-openai" {
		b, _ := json.Marshal(e)
		t.Fatalf("event: %s", b)
	}
}

func TestProxyStreamingAndHostHeader(t *testing.T) {
	up, px, got, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Openrouter-Provider", "DeepInfra")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"gen-1\",\"model\":\"m/x\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		f.Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"gen-1\",\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"cost\":0.0001}}\n\ndata: [DONE]\n\n")
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	res, err := http.Post(srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m/x","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body)
	e := wait(t, mu, got)
	if *e.Stream != true || *e.CostUSD != 0.0001 || e.AmountBasis != event.BasisProviderReported || e.ServedHost != "deepinfra" || e.HostEvidence != "response_header" || e.TTFTMs == nil {
		b, _ := json.Marshal(e)
		t.Fatalf("event: %s", b)
	}
}

func TestProxyClientAbort(t *testing.T) {
	upstreamDone := make(chan struct{})
	up, px, got, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"model\":\"m\"}\n\n")
		f.Flush()
		<-r.Context().Done() // the proxy must tear the upstream down
		close(upstreamDone)
	})
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(px)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/proxy/"+u.Host+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_, _ = res.Body.Read(buf)
	cancel()
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not cancelled")
	}
	e := wait(t, mu, got)
	if !e.Cancelled || e.Complete || e.StatusClass != "cancelled" {
		b, _ := json.Marshal(e)
		t.Fatalf("event: %s", b)
	}
}

func TestLoopbackOnlyHost(t *testing.T) {
	px := New(Options{InstallID: "r_0123456789abcdef"})
	srv := httptest.NewServer(px)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/openai/v1/models", nil)
	req.Host = "evil.example"
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("status %d", res.StatusCode)
	}
}
