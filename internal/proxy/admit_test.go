package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
)

// anthropicRoute points the named Anthropic route at a fake upstream.
func anthropicRoute(t *testing.T, up *httptest.Server, rest string) Route {
	t.Helper()
	host, portS, _ := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	port, _ := strconv.Atoi(portS)
	return Route{Upstream: Upstream{Host: host, Port: port, Insecure: true, Style: StyleAnthropic, Direct: true}, Provider: "anthropic", Rest: rest}
}

// A Claude Code messages request as the client writes it (2.1.284): the
// model first, the metadata after the messages, its user_id a JSON string
// with the session id inside.
const claudeBody = `{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hi"}],"system":"x","tools":[],"metadata":{"user_id":"{\"device_id\":\"abc\",\"account_uuid\":\"9f0c1e5a-1111-2222-3333-444444444444\",\"session_id\":\"F7D5EC30-6DF2-4EA4-A3A1-7583E44F39C2\"}"},"max_tokens":8}`

// Older clients wrote the ids into one underscored string.
const legacyBody = `{"model":"claude-fable-5-1","messages":[],"metadata":{"user_id":"user_abc_account_9f0c1e5a-1111-2222-3333-444444444444_session_f7d5ec30-6df2-4ea4-a3a1-7583e44f39c2"}}`

func TestSessionIsFoundInBothMetadataShapes(t *testing.T) {
	for _, body := range []string{claudeBody, legacyBody} {
		m := sessionInBody.FindStringSubmatch(body)
		if m == nil || strings.ToLower(m[1]) != "f7d5ec30-6df2-4ea4-a3a1-7583e44f39c2" {
			t.Fatalf("session not found in %s", body)
		}
	}
}

func TestAdmitJudgesTheRequestsOwnModelAndSession(t *testing.T) {
	var seenBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-fable-5-1","usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer up.Close()
	var mu sync.Mutex
	var got []*event.Event
	var asked []AdmitRequest
	px := New(Options{InstallID: "r_0123456789abcdef", Log: func(s string) { t.Log(s) }, Sink: func(e *event.Event) { mu.Lock(); got = append(got, e); mu.Unlock() }, Admit: func(q AdmitRequest) *Refusal { asked = append(asked, q); return nil }})
	req := httptest.NewRequest("POST", "http://127.0.0.1/anthropic/v1/messages", strings.NewReader(claudeBody))
	req.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
	rec := httptest.NewRecorder()
	px.proxy(rec, req, anthropicRoute(t, up, "/v1/messages"))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(asked) != 1 {
		t.Fatalf("admission asked %d times", len(asked))
	}
	if asked[0].Model != "claude-fable-5-1" || asked[0].Session != "f7d5ec30-6df2-4ea4-a3a1-7583e44f39c2" || asked[0].Harness != "claude-cli" {
		t.Fatalf("admission saw %+v", asked[0])
	}
	if string(seenBody) != claudeBody {
		t.Fatalf("upstream body changed: %s", seenBody)
	}
	e := wait(t, &mu, &got)
	if e.RequestedModel != "claude-fable-5-1" {
		t.Fatalf("recorded model %q", e.RequestedModel)
	}
}

func TestARefusedRequestNeverReachesTheProviderOrTheRecord(t *testing.T) {
	upstreamCalls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls++ }))
	defer up.Close()
	var mu sync.Mutex
	var got []*event.Event
	px := New(Options{InstallID: "r_0123456789abcdef", Sink: func(e *event.Event) { mu.Lock(); got = append(got, e); mu.Unlock() }, Admit: func(q AdmitRequest) *Refusal {
		return &Refusal{Message: "the weekly bucket for fable is at 100%"}
	}})
	req := httptest.NewRequest("POST", "http://127.0.0.1/anthropic/v1/messages", strings.NewReader(claudeBody))
	rec := httptest.NewRecorder()
	px.proxy(rec, req, anthropicRoute(t, up, "/v1/messages"))
	if rec.Code != 403 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"permission_error"`) || !strings.Contains(body, "fable is at 100%") {
		t.Fatalf("refusal body %s", body)
	}
	px.Wait()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if upstreamCalls != 0 || len(got) != 0 {
		t.Fatalf("refused request reached upstream %d times and recorded %d events", upstreamCalls, len(got))
	}
}

func TestAdmitSkipsTokenCountingAndNonAnthropicCalls(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":3}`))
	}))
	defer up.Close()
	asked := 0
	px := New(Options{InstallID: "r_0123456789abcdef", Sink: func(*event.Event) {}, Admit: func(AdmitRequest) *Refusal { asked++; return &Refusal{Message: "no"} }})
	req := httptest.NewRequest("POST", "http://127.0.0.1/anthropic/v1/messages/count_tokens", strings.NewReader(claudeBody))
	rec := httptest.NewRecorder()
	px.proxy(rec, req, anthropicRoute(t, up, "/v1/messages/count_tokens"))
	if rec.Code != 200 || asked != 0 {
		t.Fatalf("count_tokens: status %d, asked %d", rec.Code, asked)
	}
	rt := anthropicRoute(t, up, "/v1/chat/completions")
	rt.Upstream.Style = StyleOpenAI
	req = httptest.NewRequest("POST", "http://127.0.0.1/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[]}`))
	rec = httptest.NewRecorder()
	px.proxy(rec, req, rt)
	if rec.Code != 200 || asked != 0 {
		t.Fatalf("openai: status %d, asked %d", rec.Code, asked)
	}
}
