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
