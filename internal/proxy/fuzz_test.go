package proxy

import "testing"

func FuzzScan(f *testing.F) {
	f.Add([]byte(`{"id":"a","model":"m","usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	f.Add([]byte(`{"response":{"id":"r","usage":{"input_tokens":3}}}`))
	f.Add([]byte(`{"a":[{"usage":{"prompt_tokens":9}}],"usage":{"prompt_tokens":1`))
	f.Add([]byte("data: {\"usage\":{\"prompt_tokens\":1}}\n\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, st := range []Style{StyleOpenAI, StyleAnthropic, StyleGoogle} {
			sc := newScan(st)
			for i := 0; i < len(b); i += 7 {
				sc.Write(b[i:min(i+7, len(b))])
			}
			_ = sc.Result()
			s := NewStreamUsage(st)
			s.Write(b)
			_ = s.Result()
			_ = TopLevelString(b, "model")
			_ = WithIncludeUsage(b)
		}
	})
}

func TestMalformedSSEBounded(t *testing.T) {
	s := NewStreamUsage(StyleOpenAI)
	// no event terminator ever: memory must stay bounded
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + string(make([]byte, 60000)) + "\"}}]}")
	for i := 0; i < 200; i++ {
		s.Write(chunk)
	}
	if len(s.partial) > maxEventBytes {
		t.Fatalf("partial grew to %d", len(s.partial))
	}
	if s.Result() != nil {
		t.Fatal("garbage produced usage")
	}
}
