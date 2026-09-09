package proxy

import "testing"

func TestStreamKeepsID(t *testing.T) {
	s := NewStreamUsage(StyleOpenAI)
	s.Write([]byte("data: {\"id\":\"c0\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"tok0 tok tok tok tok\"}}]}\n\n"))
	s.Write([]byte("data: {\"id\":\"c\",\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":30}}\n\ndata: [DONE]\n\n"))
	u := s.Result()
	if u == nil || u.RequestID == nil {
		t.Fatalf("id lost: %+v", u)
	}
	sc := newScan(StyleOpenAI)
	sc.Write([]byte(`{"id":"c0","model":"m","choices":[{"delta":{"content":"x"}}]}`))
	u2 := sc.Result()
	if u2 == nil || u2.RequestID == nil || *u2.RequestID != "c0" {
		t.Fatalf("scan id lost: %+v top=%v", u2, sc.top)
	}
}
