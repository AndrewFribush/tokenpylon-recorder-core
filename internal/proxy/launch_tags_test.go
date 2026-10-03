package proxy

import (
	"encoding/base64"
	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLaunchAttributionStaysLocal(t *testing.T) {
	up, px, _, _ := withUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "test=1" {
			t.Errorf("metadata leaked into upstream URL: %s", r.URL)
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-tokenpylon-") {
				t.Errorf("local header leaked: %s", k)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"reply","model":"test","usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	})
	var context event.Context
	px.opts.Context = func(c event.Context) { context = c }
	u, _ := url.Parse(up.URL)
	prefix := "/tag/" + base64.RawURLEncoding.EncodeToString([]byte("My project")) + "/0123456789abcdef0123456789abcdef"
	req := httptest.NewRequest("POST", "http://127.0.0.1"+prefix+"/proxy/"+u.Host+"/v1/chat/completions?test=1", strings.NewReader(`{"model":"test"}`))
	res := httptest.NewRecorder()
	px.ServeHTTP(res, req)
	px.sinks.Wait()
	if res.Code != 200 || context.Project != "My project" || context.Session != "launch-0123456789abcdef0123456789abcdef" {
		t.Fatalf("response %d context %+v", res.Code, context)
	}
	if req.Header.Get(projectHeader) != "" {
		t.Fatal("mutated original request")
	}
}

func TestLaunchTagRejectsInvalidAndHonorsExplicitTags(t *testing.T) {
	req := httptest.NewRequest("POST", "http://127.0.0.1/", nil)
	for _, path := range []string{"/tag/x/short/openai/v1", "/tag/L2V0Yw/0123456789abcdef0123456789abcdef/openai/v1"} {
		if _, _, ok := withLaunchTags(req, path); ok {
			t.Fatal("accepted malformed project tag")
		}
	}
	req.Header.Set(projectHeader, "Explicit")
	req.Header.Set(sessionHeader, "explicit-session")
	got, _, ok := withLaunchTags(req, "/tag/RGVtbw/0123456789abcdef0123456789abcdef/openai/v1")
	if !ok || got.Header.Get(projectHeader) != "Explicit" || got.Header.Get(sessionHeader) != "explicit-session" {
		t.Fatal("explicit tags lost")
	}
}
