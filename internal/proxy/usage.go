package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"
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
	// they carry usage. Either way the event is read structurally: only
	// approved paths are materialised, never content.
	if !s.first && !interesting(data) {
		return
	}
	s.first = false
	sc := newScan(s.style)
	sc.Write(data)
	s.acc.merge(sc.Result())
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

// scan is a structural, streaming JSON reader that materialises only
// approved paths: the top-level strings id/model/provider/service_tier/
// responseId/modelVersion (and the same under "response"/"message"), and
// the object under usage / usageMetadata at the top level or under
// response/message. Everything else, message content included, is walked
// byte by byte and dropped. Works on a body of any size fed in chunks.
type scan struct {
	style Style
	// tokenizer state
	inStr, esc bool
	str        []byte // current string being read (bounded)
	strIsKey   bool
	depth      int
	keys       []string // key at each depth (index = depth-1)
	expectVal  bool     // just saw ':' at the current depth
	// capture state
	capDepth int // depth at which the captured object started (0 = none)
	cap      []byte
	// results
	top      map[string]string // top-level strings
	nested   map[string]string // strings under response/message
	usage    []byte            // last captured usage object
	valid    bool              // first non-space byte was '{'
	started  bool
	overflow bool
	objs     []bool // container stack: true = object, false = array
}

const maxScanStr = 4 << 10
const maxScanObj = 64 << 10

func newScan(style Style) *scan {
	return &scan{style: style, top: map[string]string{}, nested: map[string]string{}}
}

var wantStrings = map[string]bool{"id": true, "model": true, "provider": true, "service_tier": true, "responseId": true, "modelVersion": true}

func (sc *scan) usageKey() string {
	if sc.style == StyleGoogle {
		return "usageMetadata"
	}
	return "usage"
}

func (sc *scan) parentIsEnvelope() bool {
	return sc.depth == 2 && (sc.keys[0] == "response" || sc.keys[0] == "message")
}

func (sc *scan) setKey(k string) {
	for len(sc.keys) < sc.depth {
		sc.keys = append(sc.keys, "")
	}
	sc.keys[sc.depth-1] = k
}

func (sc *scan) Write(p []byte) {
	for _, c := range p {
		if !sc.started {
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				continue
			}
			sc.started = true
			sc.valid = c == '{'
		}
		if !sc.valid {
			return
		}
		if sc.capDepth > 0 {
			sc.cap = append(sc.cap, c)
			if len(sc.cap) > maxScanObj {
				sc.overflow = true
			}
		}
		if sc.inStr {
			if len(sc.str) < maxScanStr {
				sc.str = append(sc.str, c)
			}
			if sc.esc {
				sc.esc = false
				continue
			}
			if c == '\\' {
				sc.esc = true
				continue
			}
			if c == '"' {
				sc.inStr = false
				raw := sc.str[:len(sc.str)-1]
				if sc.strIsKey {
					var k string
					if json.Unmarshal(append(append([]byte{'"'}, raw...), '"'), &k) == nil {
						sc.setKey(k)
					} else {
						sc.setKey("")
					}
				} else if sc.expectVal && sc.capDepth == 0 && len(raw) < maxScanStr {
					k := sc.keys[sc.depth-1]
					if wantStrings[k] {
						var v string
						if json.Unmarshal(append(append([]byte{'"'}, raw...), '"'), &v) == nil {
							if sc.depth == 1 {
								sc.top[k] = v
							} else if sc.parentIsEnvelope() {
								sc.nested[k] = v
							}
						}
					}
				}
				sc.expectVal = false
			}
			continue
		}
		switch c {
		case '"':
			sc.inStr, sc.esc = true, false
			sc.str = sc.str[:0]
			sc.strIsKey = !sc.expectVal && sc.depth > 0 && sc.capDepth == 0 && sc.inObject()
		case '{':
			sc.depth++
			sc.setKey("")
			if sc.capDepth == 0 && sc.expectVal && sc.depth >= 2 {
				parentKey := sc.keys[sc.depth-2]
				if parentKey == sc.usageKey() && (sc.depth == 2 || (sc.depth == 3 && (sc.keys[0] == "response" || sc.keys[0] == "message"))) {
					sc.capDepth = sc.depth
					sc.cap = append(sc.cap[:0], '{')
					sc.overflow = false
				}
			}
			sc.expectVal = false
			sc.objs = append(sc.objs, true)
		case '[':
			sc.depth++
			sc.setKey("")
			sc.expectVal = false
			sc.objs = append(sc.objs, false)
		case '}', ']':
			if sc.capDepth == sc.depth && c == '}' {
				if !sc.overflow {
					sc.usage = append([]byte(nil), sc.cap...)
				}
				sc.capDepth = 0
			}
			if sc.depth > 0 {
				sc.depth--
			}
			if len(sc.objs) > 0 {
				sc.objs = sc.objs[:len(sc.objs)-1]
			}
			sc.expectVal = false
		case ':':
			if sc.inObject() {
				sc.expectVal = true
			}
		case ',':
			sc.expectVal = false
		}
	}
}

func (sc *scan) inObject() bool { return len(sc.objs) > 0 && sc.objs[len(sc.objs)-1] }

// Result assembles a Usage from what was approved.
func (sc *scan) Result() *Usage {
	if !sc.valid {
		return nil
	}
	env := map[string]any{}
	for k, v := range sc.top {
		env[k] = v
	}
	if len(sc.nested) > 0 {
		n := map[string]any{}
		for k, v := range sc.nested {
			n[k] = v
		}
		env["response"] = n
	}
	if sc.usage != nil {
		if o := decode(sc.usage); o != nil {
			env[sc.usageKey()] = o
		}
	}
	u := Extract(sc.style, env)
	if u == nil || (!u.any && u.Model == nil && u.RequestID == nil) {
		return nil
	}
	return u
}

// BodyUsage: usage from a non-streaming JSON body of any size, structurally.
type BodyUsage struct{ sc *scan }

func NewBodyUsage(style Style) *BodyUsage { return &BodyUsage{sc: newScan(style)} }
func (bu *BodyUsage) Write(p []byte)      { bu.sc.Write(p) }
func (bu *BodyUsage) Result() *Usage      { return bu.sc.Result() }

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
	r := &recorder{ch: make(chan []byte, 1024), done: make(chan struct{})}
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
	if r.drop {
		return // once a chunk is lost the rest is meaningless for parsing
	}
	c := make([]byte, len(p))
	copy(c, p)
	select {
	case r.ch <- c:
	default:
		r.drop = true
	}
}

// Close drains and waits up to d. Returns false when recording is not
// reliable: a chunk was dropped, or the parser did not finish in time.
func (r *recorder) Close(d time.Duration) bool {
	r.once.Do(func() { close(r.ch) })
	select {
	case <-r.done:
		return !r.drop
	case <-time.After(d):
		return false
	}
}
