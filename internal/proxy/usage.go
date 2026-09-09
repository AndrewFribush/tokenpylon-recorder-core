package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Usage is what an adapter extracts from a response. Nil = not reported.
type Usage struct {
	Model, RequestID                             *string
	Input, Cached, CacheWrite, Output, Reasoning *int64
	ReasoningInOutput                            *bool
	CachedInInput                                *bool
	CostUSD                                      *float64
	ServedHost                                   *string // OpenRouter puts the host in the body
	ServiceTier                                  *string
	any                                          bool
}

func (u *Usage) merge(o *Usage) {
	if o == nil {
		return
	}
	u.any = u.any || o.any
	if o.Model != nil {
		u.Model = o.Model
	}
	if o.RequestID != nil {
		u.RequestID = o.RequestID
	}
	if o.Input != nil {
		u.Input = o.Input
	}
	if o.Cached != nil {
		u.Cached = o.Cached
	}
	if o.CacheWrite != nil {
		u.CacheWrite = o.CacheWrite
	}
	if o.Output != nil {
		u.Output = o.Output
	}
	if o.Reasoning != nil {
		u.Reasoning = o.Reasoning
	}
	if o.ReasoningInOutput != nil {
		u.ReasoningInOutput = o.ReasoningInOutput
	}
	if o.CachedInInput != nil {
		u.CachedInInput = o.CachedInInput
	}
	if o.CostUSD != nil {
		u.CostUSD = o.CostUSD
	}
	if o.ServedHost != nil {
		u.ServedHost = o.ServedHost
	}
	if o.ServiceTier != nil {
		u.ServiceTier = o.ServiceTier
	}
}

func num(v any) *int64 {
	switch n := v.(type) {
	case float64:
		if n >= 0 {
			i := int64(n)
			return &i
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i >= 0 {
			return &i
		}
	}
	return nil
}
func fnum(v any) *float64 {
	if n, ok := v.(float64); ok && n >= 0 {
		return &n
	}
	return nil
}
func str(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
func i64(v int64) *int64 { return &v }
func b(v bool) *bool     { return &v }

// Extract reads the usage of one JSON object (a whole response, or one SSE
// event) for the given style. Model and id are taken only from the
// top-level object or its nested response/message, never by string search.
func Extract(style Style, j map[string]any) *Usage {
	if j == nil {
		return nil
	}
	u := &Usage{}
	nested := obj(j["response"])
	if nested == nil {
		nested = obj(j["message"])
	}
	u.Model = str(j["model"])
	if u.Model == nil && nested != nil {
		u.Model = str(nested["model"])
	}
	if u.Model == nil {
		u.Model = str(j["modelVersion"])
	}
	u.RequestID = str(j["id"])
	if u.RequestID == nil && nested != nil {
		u.RequestID = str(nested["id"])
	}
	if u.RequestID == nil {
		u.RequestID = str(j["responseId"])
	}
	u.ServiceTier = str(j["service_tier"])
	if p := str(j["provider"]); p != nil { // OpenRouter
		s := strings.ToLower(*p)
		u.ServedHost = &s
	}
	switch style {
	case StyleGoogle:
		m := obj(j["usageMetadata"])
		if m == nil {
			return u
		}
		u.any = true
		cached := num(m["cachedContentTokenCount"])
		if prompt := num(m["promptTokenCount"]); prompt != nil {
			c := int64(0)
			if cached != nil {
				c = *cached
			}
			u.Input = i64(max(0, *prompt-c))
			u.Cached = i64(c)
			u.CachedInInput = b(true)
		}
		out := num(m["candidatesTokenCount"])
		th := num(m["thoughtsTokenCount"])
		if out != nil {
			o := *out
			if th != nil {
				o += *th
			}
			u.Output = i64(o)
			u.ReasoningInOutput = b(true)
		}
		u.Reasoning = th
		return u
	case StyleAnthropic:
		m := obj(j["usage"])
		if m == nil && nested != nil {
			m = obj(nested["usage"])
		}
		if m == nil {
			return u
		}
		u.any = true
		u.Input = num(m["input_tokens"])
		u.Cached = num(m["cache_read_input_tokens"])
		u.CacheWrite = num(m["cache_creation_input_tokens"])
		u.Output = num(m["output_tokens"])
		u.CachedInInput = b(false)
		if st := str(m["service_tier"]); st != nil {
			u.ServiceTier = st
		}
		return u
	}
	m := obj(j["usage"])
	if m == nil && nested != nil {
		m = obj(nested["usage"])
	}
	if m == nil {
		return u
	}
	u.any = true
	u.CostUSD = fnum(m["cost"]) // OpenRouter with usage accounting
	if _, hasPrompt := m["prompt_tokens"]; !hasPrompt {
		if inp := num(m["input_tokens"]); inp != nil { // Responses API
			d := obj(m["input_tokens_details"])
			cached := int64(0)
			if c := num(d["cached_tokens"]); c != nil {
				cached = *c
			}
			u.Input = i64(max(0, *inp-cached))
			u.Cached = i64(cached)
			u.CachedInInput = b(true)
			u.Output = num(m["output_tokens"])
			u.Reasoning = num(obj(m["output_tokens_details"])["reasoning_tokens"])
			if u.Reasoning != nil {
				u.ReasoningInOutput = b(true)
			}
			return u
		}
	}
	d := obj(m["prompt_tokens_details"])
	cd := obj(m["completion_tokens_details"])
	var cached *int64
	for _, k := range []any{d["cached_tokens"], m["prompt_cache_hit_tokens"], m["cached_tokens"]} {
		if c := num(k); c != nil {
			cached = c
			break
		}
	}
	if prompt := num(m["prompt_tokens"]); prompt != nil {
		c := int64(0)
		if cached != nil {
			c = *cached
		}
		u.Input = i64(max(0, *prompt-c))
		u.Cached = i64(c)
		u.CachedInInput = b(true)
	}
	u.Output = num(m["completion_tokens"])
	u.Reasoning = num(cd["reasoning_tokens"])
	if u.Reasoning != nil {
		u.ReasoningInOutput = b(true)
	}
	return u
}

// FromBody parses a whole JSON body. Non-JSON or oversize bodies yield nil.
func FromBody(style Style, body []byte) *Usage {
	var j map[string]any
	if json.Unmarshal(body, &j) != nil {
		return nil
	}
	u := Extract(style, j)
	if u == nil || !u.any {
		return nil
	}
	return u
}

const maxEventBytes = 4 << 20

// StreamUsage folds SSE events into one Usage as they pass. Memory holds
// one partial event at most. Anthropic: input in message_start, output in
// message_delta. OpenAI: a final chunk with usage. Responses API:
// response.completed carries the whole usage block.
type StreamUsage struct {
	style   Style
	partial []byte
	acc     Usage
}

func NewStreamUsage(style Style) *StreamUsage { return &StreamUsage{style: style} }

func (s *StreamUsage) Write(p []byte) {
	s.partial = append(s.partial, p...)
	for {
		i := bytes.Index(s.partial, []byte("\n\n"))
		j := bytes.Index(s.partial, []byte("\r\n\r\n"))
		sep := 2
		if i < 0 || (j >= 0 && j < i) {
			i, sep = j, 4
		}
		if i < 0 {
			break
		}
		s.event(s.partial[:i])
		s.partial = s.partial[i+sep:]
	}
	if len(s.partial) > maxEventBytes {
		s.partial = s.partial[:0]
	}
}

func (s *StreamUsage) event(e []byte) {
	var data []byte
	for _, line := range bytes.Split(e, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, bytes.TrimSpace(line[5:])...)
		}
	}
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	var j map[string]any
	if json.Unmarshal(data, &j) != nil {
		return
	}
	s.acc.merge(Extract(s.style, j))
}

func (s *StreamUsage) Result() *Usage {
	if len(s.partial) > 0 {
		s.event(s.partial)
		s.partial = nil
	}
	if !s.acc.any {
		if s.acc.Model == nil && s.acc.RequestID == nil {
			return nil
		}
	}
	return &s.acc
}

// TopLevelModel reads the top-level "model" of a JSON request body prefix
// with a depth-tracking scan, so a nested "model" inside messages is never
// taken and nothing else is read.
func TopLevelModel(head []byte) *string {
	depth, inStr, esc := 0, false, false
	var key []byte
	collectingKey, expectModel := false, false
	for i := 0; i < len(head); i++ {
		c := head[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
				if collectingKey {
					collectingKey = false
				} else if expectModel && depth == 1 {
					s := string(key)
					if len(s) > 200 {
						s = s[:200]
					}
					return &s
				}
				continue
			}
			key = append(key, c)
			continue
		}
		switch c {
		case '"':
			inStr = true
			key = key[:0]
			if !(expectModel && depth == 1) {
				collectingKey = true
			}
		case '{', '[':
			depth++
			expectModel = false
		case '}', ']':
			depth--
			expectModel = false
		case ':':
			if depth == 1 && !collectingKey {
				expectModel = string(key) == "model"
				key = key[:0]
			}
		case ',':
			expectModel = false
		}
	}
	return nil
}

// WithIncludeUsage rewrites an OpenAI chat request so a stream carries
// usage. Returns the original bytes when nothing needs changing or the
// body is not a JSON object.
func WithIncludeUsage(body []byte) []byte {
	var j map[string]any
	if json.Unmarshal(body, &j) != nil {
		return body
	}
	if st, _ := j["stream"].(bool); !st {
		return body
	}
	so, _ := j["stream_options"].(map[string]any)
	if so != nil {
		if _, has := so["include_usage"]; has {
			return body
		}
	} else {
		so = map[string]any{}
	}
	so["include_usage"] = true
	j["stream_options"] = so
	out, err := json.Marshal(j)
	if err != nil {
		return body
	}
	return out
}
