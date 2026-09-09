package proxy

import (
	"testing"
)

func TestExtractOpenAIChat(t *testing.T) {
	u := FromBody(StyleOpenAI, []byte(`{"id":"chatcmpl-1","model":"gpt-5","usage":{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":100},"completion_tokens_details":{"reasoning_tokens":5}}}`))
	if u == nil || *u.Input != 20 || *u.Cached != 100 || *u.Output != 30 || *u.Reasoning != 5 || *u.RequestID != "chatcmpl-1" || *u.Model != "gpt-5" {
		t.Fatalf("bad: %+v", u)
	}
}

func TestExtractResponses(t *testing.T) {
	u := FromBody(StyleOpenAI, []byte(`{"id":"resp_1","model":"gpt-5","usage":{"input_tokens":50,"input_tokens_details":{"cached_tokens":10},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":2}}}`))
	if u == nil || *u.Input != 40 || *u.Cached != 10 || *u.Output != 7 || *u.Reasoning != 2 {
		t.Fatalf("bad: %+v", u)
	}
}

func TestExtractOpenRouterCost(t *testing.T) {
	u := FromBody(StyleOpenAI, []byte(`{"id":"gen-1","provider":"Novita","model":"deepseek/deepseek-v4","usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00012}}`))
	if u == nil || u.CostUSD == nil || *u.CostUSD != 0.00012 || *u.ServedHost != "novita" {
		t.Fatalf("bad: %+v", u)
	}
}

func TestAnthropicStream(t *testing.T) {
	s := NewStreamUsage(StyleAnthropic)
	s.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":25,\"cache_creation_input_tokens\":100,\"cache_read_input_tokens\":2000,\"output_tokens\":1}}}\n\n"))
	s.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n"))
	s.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n"))
	u := s.Result()
	if u == nil || *u.Input != 25 || *u.CacheWrite != 100 || *u.Cached != 2000 || *u.Output != 42 || *u.RequestID != "msg_1" || *u.Model != "claude-opus-5" {
		t.Fatalf("bad: %+v", u)
	}
}

func TestOpenAIStreamSplitChunks(t *testing.T) {
	s := NewStreamUsage(StyleOpenAI)
	full := "data: {\"id\":\"chatcmpl-9\",\"model\":\"gpt-5-mini\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: {\"id\":\"chatcmpl-9\",\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"
	for i := 0; i < len(full); i += 7 {
		s.Write([]byte(full[i:min(i+7, len(full))]))
	}
	u := s.Result()
	if u == nil || *u.Input != 9 || *u.Output != 3 || *u.Model != "gpt-5-mini" {
		t.Fatalf("bad: %+v", u)
	}
}

func TestGoogle(t *testing.T) {
	u := FromBody(StyleGoogle, []byte(`{"responseId":"r1","modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":40,"candidatesTokenCount":10,"thoughtsTokenCount":6}}`))
	if u == nil || *u.Input != 60 || *u.Cached != 40 || *u.Output != 16 || *u.Reasoning != 6 || *u.Model != "gemini-3-pro" {
		t.Fatalf("bad: %+v", u)
	}
}

func TestTopLevelModel(t *testing.T) {
	m := TopLevelModel([]byte(`{"messages":[{"role":"user","content":"my model is x","model":"nested"}],"model":"gpt-5","stream":true`))
	if m == nil || *m != "gpt-5" {
		t.Fatalf("got %v", m)
	}
	if TopLevelModel([]byte(`{"messages":[{"model":"nested"}]}`)) != nil {
		t.Fatal("nested model taken")
	}
}

func TestWithIncludeUsage(t *testing.T) {
	out := string(WithIncludeUsage([]byte(`{"model":"m","stream":true,"messages":[]}`)))
	if out == `{"model":"m","stream":true,"messages":[]}` || !contains(out, `"include_usage":true`) {
		t.Fatalf("not rewritten: %s", out)
	}
	same := []byte(`{"model":"m","stream":false}`)
	if string(WithIncludeUsage(same)) != string(same) {
		t.Fatal("non-stream rewritten")
	}
	keep := []byte(`{"model":"m","stream":true,"stream_options":{"include_usage":false}}`)
	if string(WithIncludeUsage(keep)) != string(keep) {
		t.Fatal("explicit choice overridden")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestResolve(t *testing.T) {
	r, ok := Resolve("/openai/v1/chat/completions", false)
	if !ok || r.Provider != "openai" || r.Rest != "/v1/chat/completions" {
		t.Fatalf("bad: %+v", r)
	}
	if _, ok := Resolve("/proxy/127.0.0.1:4000/v1/chat/completions", false); ok {
		t.Fatal("loopback allowed by default")
	}
	r, ok = Resolve("/proxy/127.0.0.1:4000/v1/chat/completions", true)
	if !ok || !r.Upstream.Insecure || r.Upstream.Port != 4000 {
		t.Fatalf("bad: %+v", r)
	}
	r, ok = Resolve("/proxy/api.example-host.com/v1/chat/completions", false)
	if !ok || r.Provider != "example-host" || r.Upstream.Host != "api.example-host.com" {
		t.Fatalf("bad: %+v", r)
	}
	if _, ok := Resolve("/nope/v1", false); ok {
		t.Fatal("unknown accepted")
	}
}
