package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTokenCountPassesThroughWithoutInferenceEvent(t *testing.T) {
	up, px, got, mu := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	})
	u, _ := url.Parse(up.URL)
	req := httptest.NewRequest("POST", "http://127.0.0.1/proxy/"+u.Host+"/v1/messages/count_tokens?beta=true", strings.NewReader(`{"model":"claude-opus-5","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	px.ServeHTTP(res, req)
	px.sinks.Wait()
	if res.Code != 200 || res.Body.String() != `{"input_tokens":42}` {
		t.Fatalf("response: %d %s", res.Code, res.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 0 {
		t.Fatalf("token count recorded as %d inference events", len(*got))
	}
}

// Tool search requires nested references and deferred definitions to survive the proxy.
func TestToolSearchReferencesPassThrough(t *testing.T) {
	const body = `{"model":"claude-opus-4-6","max_tokens":32,"tools":[{"name":"lookup","description":"Find records","input_schema":{"type":"object"},"defer_loading":true}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"lookup"}]}]}]}`
	const response = `{"type":"message","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}],"usage":{"input_tokens":20,"output_tokens":10}}`
	up, px, _, _ := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil || string(b) != body {
			t.Errorf("tool search payload changed: %s (%v)", b, err)
		}
		if r.Header.Get("Anthropic-Beta") != "advanced-tool-use-2025-11-20" {
			t.Error("tool search beta header lost")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	})
	u, _ := url.Parse(up.URL)
	req := httptest.NewRequest("POST", "http://127.0.0.1/proxy/"+u.Host+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "advanced-tool-use-2025-11-20")
	res := httptest.NewRecorder()
	px.ServeHTTP(res, req)
	px.sinks.Wait()
	if res.Code != 200 || res.Body.String() != response {
		t.Fatalf("tool response changed: %d %s", res.Code, res.Body.String())
	}
}
