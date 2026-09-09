package proxy

import (
	"regexp"
	"strings"
)

// Style selects the response adapter.
type Style string

const (
	StyleOpenAI    Style = "openai"    // chat/completions, responses, embeddings, and everything OpenAI-compatible
	StyleAnthropic Style = "anthropic" // messages
	StyleGoogle    Style = "google"    // generateContent (usageMetadata)
)

type Upstream struct {
	Host     string
	Port     int
	Insecure bool // plain HTTP; loopback upstreams only
	BasePath string
	Style    Style
	Direct   bool // the provider serves its own models: host evidence "direct_provider"
}

// Upstreams: path prefix -> upstream.
var Upstreams = map[string]Upstream{
	"openai":         {Host: "api.openai.com", Style: StyleOpenAI, Direct: true},
	"anthropic":      {Host: "api.anthropic.com", Style: StyleAnthropic, Direct: true},
	"gemini":         {Host: "generativelanguage.googleapis.com", Style: StyleGoogle, Direct: true},
	"openrouter":     {Host: "openrouter.ai", BasePath: "/api", Style: StyleOpenAI},
	"hf-router":      {Host: "router.huggingface.co", Style: StyleOpenAI},
	"deepseek":       {Host: "api.deepseek.com", Style: StyleOpenAI, Direct: true},
	"moonshot":       {Host: "api.moonshot.ai", Style: StyleOpenAI, Direct: true},
	"zai":            {Host: "api.z.ai", BasePath: "/api/paas", Style: StyleOpenAI, Direct: true},
	"minimax":        {Host: "api.minimax.io", Style: StyleOpenAI, Direct: true},
	"dashscope_intl": {Host: "dashscope-intl.aliyuncs.com", BasePath: "/compatible-mode", Style: StyleOpenAI, Direct: true},
	"byteplus":       {Host: "ark.ap-southeast.bytepluses.com", BasePath: "/api", Style: StyleOpenAI, Direct: true},
	"siliconflow":    {Host: "api.siliconflow.com", Style: StyleOpenAI, Direct: true},
	"stepfun":        {Host: "api.stepfun.com", Style: StyleOpenAI, Direct: true},
	"01ai":           {Host: "api.01.ai", Style: StyleOpenAI, Direct: true},
	"groq":           {Host: "api.groq.com", BasePath: "/openai", Style: StyleOpenAI, Direct: true},
	"cerebras":       {Host: "api.cerebras.ai", Style: StyleOpenAI, Direct: true},
	"together_ai":    {Host: "api.together.xyz", Style: StyleOpenAI, Direct: true},
	"fireworks_ai":   {Host: "api.fireworks.ai", BasePath: "/inference", Style: StyleOpenAI, Direct: true},
	"deepinfra":      {Host: "api.deepinfra.com", BasePath: "/v1/openai", Style: StyleOpenAI, Direct: true},
	"xai":            {Host: "api.x.ai", Style: StyleOpenAI, Direct: true},
	"mistral":        {Host: "api.mistral.ai", Style: StyleOpenAI, Direct: true},
	"perplexity":     {Host: "api.perplexity.ai", Style: StyleOpenAI, Direct: true},
}

// Gateways: upstreams that route to other hosts. Their name goes in
// `gateway`; the billed provider is still the gateway (it charges you).
var Gateways = map[string]bool{"openrouter": true, "hf-router": true}

// Route is a resolved request target.
type Route struct {
	Upstream Upstream
	Provider string // billing provider key
	Rest     string // path after the prefix
}

var genericRe = regexp.MustCompile(`^/([a-z0-9.-]+)(?::(\d+))?(/.*)?$`)

func isLoopback(host string) bool {
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

// Resolve maps a request path to a route. `/proxy/<host>[:port]/...`
// reaches anything OpenAI-shaped over HTTPS; a loopback host is plain HTTP
// and only allowed when allowLoopback is set (LiteLLM/Ollama in front).
func Resolve(path string, allowLoopback bool) (Route, bool) {
	path = strings.TrimPrefix(path, "/")
	head, rest, _ := strings.Cut(path, "/")
	rest = "/" + rest
	if head == "proxy" {
		m := genericRe.FindStringSubmatch(rest)
		if m == nil {
			return Route{}, false
		}
		host := strings.ToLower(m[1])
		port := 0
		if m[2] != "" {
			for _, c := range m[2] {
				port = port*10 + int(c-'0')
			}
			if port < 1 || port > 65535 {
				return Route{}, false
			}
		}
		loop := isLoopback(host)
		if loop && !allowLoopback {
			return Route{}, false
		}
		provider := strings.TrimPrefix(host, "api.")
		if i := strings.LastIndex(provider, "."); i > 0 {
			provider = provider[:i]
		}
		provider = regexp.MustCompile(`[^a-z0-9_.-]`).ReplaceAllString(provider, "_")
		r := "/"
		if m[3] != "" {
			r = m[3]
		}
		return Route{Upstream: Upstream{Host: host, Port: port, Insecure: loop, Style: StyleOpenAI, Direct: true}, Provider: provider, Rest: r}, true
	}
	u, ok := Upstreams[head]
	if !ok {
		return Route{}, false
	}
	return Route{Upstream: u, Provider: head, Rest: rest}, true
}

// Operation and Mode from the path.
func operationOf(rest string) (op, mode string) {
	switch {
	case strings.Contains(rest, "/chat/completions"):
		return "chat", "chat"
	case strings.Contains(rest, "/responses"):
		return "responses", "chat"
	case strings.Contains(rest, "/messages"):
		return "messages", "chat"
	case strings.Contains(rest, "/embeddings"):
		return "embeddings", "embedding"
	case strings.Contains(rest, "/rerank"):
		return "rerank", "rerank"
	case strings.Contains(rest, "/images"):
		return "images", "image_generation"
	case strings.Contains(rest, "/audio/speech"):
		return "speech", "audio_speech"
	case strings.Contains(rest, "/audio/transcriptions"):
		return "transcription", "audio_transcription"
	case strings.Contains(rest, ":generateContent") || strings.Contains(rest, ":streamGenerateContent"):
		return "chat", "chat"
	case strings.Contains(rest, "/completions"):
		return "chat", "chat"
	}
	return "other", "other"
}
