package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
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
		if f, err := n.Float64(); err == nil && f >= 0 {
			i := int64(f)
			return &i
		}
	}
	return nil
}
func fnum(v any) *float64 {
	switch n := v.(type) {
	case float64:
		if n >= 0 {
			return &n
		}
	case json.Number:
		if f, err := n.Float64(); err == nil && f >= 0 {
			return &f
		}
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

// split takes a prompt total and an optional cached count. Absent stays
// absent: a provider that did not report cache reads is not one that
// reported zero.
func split(prompt, cached *int64) (input, cachedOut *int64) {
	if prompt == nil {
		return nil, cached
	}
	if cached == nil {
		return prompt, nil
	}
	return i64(max(0, *prompt-*cached)), cached
}

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
		u.Input, u.Cached = split(num(m["promptTokenCount"]), num(m["cachedContentTokenCount"]))
		if u.Input != nil {
			u.CachedInInput = b(true)
		}
		out := num(m["candidatesTokenCount"])
		th := num(m["thoughtsTokenCount"])
		if out != nil {
			o := *out
			if th != nil {
				o += *th
				u.ReasoningInOutput = b(true)
			}
			u.Output = i64(o)
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
		if u.Input != nil {
			u.CachedInInput = b(false)
		}
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
			u.Input, u.Cached = split(inp, num(obj(m["input_tokens_details"])["cached_tokens"]))
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
	u.Input, u.Cached = split(num(m["prompt_tokens"]), cached)
	if u.Input != nil {
		u.CachedInInput = b(true)
	}
	u.Output = num(m["completion_tokens"])
	u.Reasoning = num(cd["reasoning_tokens"])
	if u.Reasoning != nil {
		u.ReasoningInOutput = b(true)
	}
	return u
}

func decode(data []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var j map[string]any
	if dec.Decode(&j) != nil {
		return nil
	}
	return j
}

// FromBody parses a whole JSON body. Non-JSON bodies yield nil.
func FromBody(style Style, body []byte) *Usage {
	u := Extract(style, decode(body))
	if u == nil || !u.any {
		return nil
	}
	return u
}

const maxEventBytes = 4 << 20

// markers: an SSE event is only decoded when it can carry usage or the
// message envelope. Content deltas (the bulk of a stream) are never
// materialised.
var markers = [][]byte{[]byte(`"usage"`), []byte(`"usageMetadata"`), []byte(`"message_start"`), []byte(`"response.completed"`), []byte(`"response.incomplete"`), []byte(`"provider"`)}

func interesting(data []byte) bool {
	for _, m := range markers {
		if bytes.Contains(data, m) {
			return true
		}
	}
	return false
}

// StreamUsage folds SSE events into one Usage as they pass. Memory holds
// one partial event at most. Anthropic: input in message_start, output in
// message_delta. OpenAI: a final chunk with usage. Responses API:
// response.completed carries the whole usage block.
type StreamUsage struct {
	style   Style
	partial []byte
	acc     Usage
	first   bool
}

func NewStreamUsage(style Style) *StreamUsage { return &StreamUsage{style: style, first: true} }

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
	// The first event carries id and model; later ones matter only when
	// they carry usage.
	if !s.first && !interesting(data) {
		return
	}
	s.first = false
	if j := decode(data); j != nil {
		s.acc.merge(Extract(s.style, j))
	}
}

func (s *StreamUsage) Result() *Usage {
	if len(s.partial) > 0 {
		s.event(s.partial)
		s.partial = nil
	}
	if !s.acc.any && s.acc.Model == nil && s.acc.RequestID == nil {
		return nil
	}
	return &s.acc
}

// BodyUsage extracts usage from a non-streaming body of any size without
// holding it: the head (first 16 KB) yields the top-level id/model/provider;
// the balanced object after the LAST "usage" / "usageMetadata" key is
// captured as it passes (bounded), so a 10 MB embeddings response still
// yields its counts.
type BodyUsage struct {
	style Style
	head  []byte
	key   []byte
	// capture state
	buf     []byte // last complete usage object
	cur     []byte // object being captured
	depth   int
	inStr   bool
	esc     bool
	matchAt int // progress matching the key
	seek    bool
	total   int
}

const headBytes = 16 << 10
const maxUsageObj = 64 << 10

func NewBodyUsage(style Style) *BodyUsage {
	k := []byte(`"usage"`)
	if style == StyleGoogle {
		k = []byte(`"usageMetadata"`)
	}
	return &BodyUsage{style: style, key: k}
}

func (bu *BodyUsage) Write(p []byte) {
	if len(bu.head) < headBytes {
		bu.head = append(bu.head, p[:min(len(p), headBytes-len(bu.head))]...)
	}
	for _, c := range p {
		if bu.depth > 0 { // capturing
			bu.cur = append(bu.cur, c)
			if len(bu.cur) > maxUsageObj {
				bu.depth, bu.cur = 0, nil
				continue
			}
			if bu.inStr {
				if bu.esc {
					bu.esc = false
				} else if c == '\\' {
					bu.esc = true
				} else if c == '"' {
					bu.inStr = false
				}
				continue
			}
			switch c {
			case '"':
				bu.inStr = true
			case '{':
				bu.depth++
			case '}':
				bu.depth--
				if bu.depth == 0 {
					bu.buf, bu.cur = bu.cur, nil
				}
			}
			continue
		}
		if bu.seek { // key matched; waiting for ':' then '{'
			if c == '{' {
				bu.seek, bu.depth, bu.cur = false, 1, []byte{'{'}
			} else if c != ':' && c != ' ' && c != '\n' && c != '\r' && c != '\t' {
				bu.seek = false
			}
			continue
		}
		if c == bu.key[bu.matchAt] {
			bu.matchAt++
			if bu.matchAt == len(bu.key) {
				bu.matchAt, bu.seek = 0, true
			}
		} else if c == bu.key[0] {
			bu.matchAt = 1
		} else {
			bu.matchAt = 0
		}
	}
}

func (bu *BodyUsage) Result() *Usage {
	u := &Usage{}
	// Only a JSON object is scanned; anything else (HTML error page, binary)
	// yields nothing rather than a lucky match.
	if len(bytes.TrimLeft(bu.head, " \t\r\n")) == 0 || bytes.TrimLeft(bu.head, " \t\r\n")[0] != '{' {
		return nil
	}
	// Whole-body parse when the body was small enough to be in the head.
	if len(bu.head) < headBytes {
		if w := FromBody(bu.style, bu.head); w != nil {
			return w
		}
	}
	for _, k := range []string{"id", "model", "provider", "responseId", "modelVersion", "service_tier"} {
		if v := TopLevelString(bu.head, k); v != nil {
			switch k {
			case "id", "responseId":
				if u.RequestID == nil {
					u.RequestID = v
				}
			case "model", "modelVersion":
				if u.Model == nil {
					u.Model = v
				}
			case "provider":
				s := strings.ToLower(*v)
				u.ServedHost = &s
			case "service_tier":
				u.ServiceTier = v
			}
		}
	}
	if bu.buf != nil {
		if o := decode(bu.buf); o != nil {
			u.merge(Extract(bu.style, map[string]any{string(bytes.Trim(bu.key, `"`)): o}))
		}
	}
	if !u.any && u.Model == nil && u.RequestID == nil {
		return nil
	}
	return u
}

// TopLevelModel: the top-level "model" of a JSON request body prefix.
func TopLevelModel(head []byte) *string { return TopLevelString(head, "model") }

// TopLevelString reads the top-level string value of `key` from a JSON
// object prefix with a depth-tracking scan, so a nested key inside messages
// is never taken and nothing else is read. The raw string is decoded as
// JSON so escapes come out right.
func TopLevelString(head []byte, key string) *string {
	depth, inStr, esc := 0, false, false
	var raw []byte
	collectingKey, expectValue := false, false
	for i := 0; i < len(head); i++ {
		c := head[i]
		if inStr {
			raw = append(raw, c)
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
				} else if expectValue && depth == 1 {
					var s string
					if json.Unmarshal(append([]byte{'"'}, raw...), &s) != nil || s == "" {
						return nil
					}
					if len(s) > 200 {
						s = s[:200]
					}
					return &s
				}
				continue
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			raw = raw[:0]
			if !(expectValue && depth == 1) {
				collectingKey = true
			}
		case '{', '[':
			depth++
			expectValue = false
		case '}', ']':
			depth--
			expectValue = false
		case ':':
			if depth == 1 && !collectingKey {
				expectValue = string(raw[:max(0, len(raw)-1)]) == key
				raw = raw[:0]
			}
		case ',':
			expectValue = false
		}
	}
	return nil
}

// WithIncludeUsage rewrites an OpenAI chat request so a stream carries
// usage. Numbers are kept verbatim (json.Number) so nothing else in the
// request changes. Returns the original bytes when nothing needs changing
// or the body is not a JSON object.
func WithIncludeUsage(body []byte) []byte {
	j := decode(body)
	if j == nil {
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

// recorder feeds response bytes to an extractor off the data path: a
// bounded queue and one goroutine per response. When the queue is full the
// chunk is dropped for recording purposes only; the client still gets it.
type recorder struct {
	ch   chan []byte
	done chan struct{}
	once sync.Once
	drop bool
}

func newRecorder(sink func([]byte)) *recorder {
	r := &recorder{ch: make(chan []byte, 256), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for c := range r.ch {
			func() {
				defer func() { _ = recover() }()
				sink(c)
			}()
		}
	}()
	return r
}

func (r *recorder) Write(p []byte) {
	c := make([]byte, len(p))
	copy(c, p)
	select {
	case r.ch <- c:
	default:
		r.drop = true
	}
}

// Close drains and waits (bounded by the caller).
func (r *recorder) Close() { r.once.Do(func() { close(r.ch) }); <-r.done }
