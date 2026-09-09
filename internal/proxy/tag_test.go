package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestClientTags(t *testing.T) {
	h := &Handler{}
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	r, _ := http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	r.Header.Set("User-Agent", "OpenAI/Python 1.40.0")
	a := h.tagOf(r, now)
	if a.Harness != "openai" || a.Agent != "main" || a.Session == "" || a.Project != "" {
		t.Fatalf("%+v", a)
	}
	b := h.tagOf(r, now.Add(10*time.Minute)) // same client, still the same session
	c := h.tagOf(r, now.Add(40*time.Minute)) // idle past the limit: a new one
	if b.Session != a.Session || c.Session == a.Session {
		t.Fatalf("sessions: %s %s %s", a.Session, b.Session, c.Session)
	}
	r2, _ := http.NewRequest("POST", "/anthropic/v1/messages", nil)
	r2.Header.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	r2.Header.Set("X-Tokenpylon-Session", "sess 42")
	r2.Header.Set("X-Tokenpylon-Agent", "planner")
	r2.Header.Set("X-Tokenpylon-Project", "tokenpylon")
	r2.Header.Set("X-Tokenpylon-Harness", "My Agent <script>")
	d := h.tagOf(r2, now)
	if d.Harness != "My_Agent__script_" || d.Session != "sess_42" || d.Agent != "planner" || d.Project != "tokenpylon" {
		t.Fatalf("%+v", d)
	}
	if harnessFromUA("") != "proxy" || harnessFromUA("python-requests/2.32") != "python-requests" || harnessFromUA("codex_cli_rs/0.153.4") != "codex_cli_rs" {
		t.Fatal("harnessFromUA")
	}
}
