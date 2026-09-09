// Package event defines the recorder's observation record, schema v2: one
// row per attempt of an LLM API call, as the provider reported it. Sizes,
// cost, timing, route, status. Never message bodies, never anything
// tokenised locally. Unknown is nil, never zero.
package event

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 2
const RecorderVersion = "0.2.0"

// AmountBasis says where a dollar figure came from.
const (
	BasisProviderReported = "provider_reported"
	BasisCatalog          = "catalog_calculated"
	BasisInferred         = "inferred"
	BasisNone             = "none"
)

// Event is the wire and storage shape. Pointer fields are nullable.
type Event struct {
	Schema          int     `json:"schema"`
	EventID         string  `json:"event_id"`
	InstallID       string  `json:"install_id"`
	Adapter         string  `json:"adapter"` // proxy-openai | proxy-anthropic | litellm | openrouter-export | litellm-export
	AdapterVersion  string  `json:"adapter_version"`
	RecorderVersion string  `json:"recorder_version"`
	OccurredAt      string  `json:"occurred_at"`
	RecordedAt      string  `json:"recorded_at"`
	TZOffsetMin     *int    `json:"tz_offset_min"`
	LogicalCallID   *string `json:"logical_call_id"` // groups retries/fallbacks of one application call
	Attempt         int     `json:"attempt"`
	Calls           int     `json:"calls"` // 1 for an observed call; n for an imported daily aggregate standing for n calls

	Provider       string  `json:"provider"`        // who bills: openai, anthropic, openrouter, ...
	Gateway        *string `json:"gateway"`         // litellm, openrouter, ... when a gateway sat in front
	RequestedModel string  `json:"requested_model"` // what the application asked for
	ReturnedModel  *string `json:"returned_model"`  // what the provider says it served
	ServedHost     string  `json:"served_host"`     // host that ran the model, or "unknown"
	HostEvidence   string  `json:"host_evidence"`   // response_header | export_column | direct_provider | none
	Operation      string  `json:"operation"`       // chat | responses | messages | embeddings | rerank | images | speech | transcription | other
	Mode           string  `json:"mode"`            // chat | embedding | rerank | image_generation | audio_speech | audio_transcription | video_generation | other

	Stream      *bool   `json:"stream"`
	Batch       *bool   `json:"batch"`
	ServiceTier *string `json:"service_tier"`

	InputTokens        *int64 `json:"input_tokens"`                    // uncached prompt tokens
	CachedTokens       *int64 `json:"cached_tokens"`                   // prompt tokens read from cache
	CacheWriteTokens   *int64 `json:"cache_write_tokens"`              // prompt tokens written to cache (every TTL)
	CacheWrite1hTokens *int64 `json:"cache_write_1h_tokens,omitempty"` // the part written at the one-hour TTL (Anthropic), nil when not reported
	OutputTokens       *int64 `json:"output_tokens"`
	ReasoningTokens    *int64 `json:"reasoning_tokens"`
	ReasoningInOutput  *bool  `json:"reasoning_in_output"` // provider counts reasoning inside output_tokens
	CachedInInput      *bool  `json:"cached_in_input"`     // provider's prompt count included cached tokens before we split

	CostUSD     *float64 `json:"cost_usd"`
	AmountBasis string   `json:"amount_basis"` // provider_reported | catalog_calculated | inferred | none
	Currency    *string  `json:"currency"`

	LatencyMs   *int   `json:"latency_ms"`
	TTFTMs      *int   `json:"ttft_ms"`
	Status      *int   `json:"status"`
	StatusClass string `json:"status_class"` // ok | auth | rate_limit | invalid_model | timeout | server | cancelled | other
	Cancelled   bool   `json:"cancelled"`
	Complete    bool   `json:"complete"` // the response (or stream) finished

	ProviderRequestID *string `json:"provider_request_id"`
}

var (
	providerRe = regexp.MustCompile(`^[a-z0-9_.-]{1,60}$`)
	modelRe    = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)
	idRe       = regexp.MustCompile(`^[A-Za-z0-9._:/+-]{1,200}$`)
	installRe  = regexp.MustCompile(`^r_[0-9a-f]{16}$`)
)

// NewInstallID mints a random install id: r_ + 16 hex.
func NewInstallID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "r_" + hex.EncodeToString(b[:])
}

// EventID is deterministic from (provider, request id) so re-imports and
// overlapping adapters collapse to one event; random otherwise.
func EventID(provider string, requestID *string, attempt int) string {
	if requestID != nil && *requestID != "" {
		h := sha256.Sum256([]byte(provider + "|" + *requestID + "|" + itoa(attempt)))
		return "e_" + hex.EncodeToString(h[:])[:24]
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "e_" + hex.EncodeToString(b[:])
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

// ShapeKey is the fallback dedup key for events without a provider request
// id: same install, provider, model, second and counts.
func (e *Event) ShapeKey() string {
	sec := e.OccurredAt
	if len(sec) > 19 {
		sec = sec[:19]
	}
	h := sha256.Sum256([]byte(strings.Join([]string{e.InstallID, e.Provider, e.RequestedModel, sec, i64s(e.InputTokens), i64s(e.CachedTokens), i64s(e.OutputTokens)}, "|")))
	return hex.EncodeToString(h[:])[:24]
}

func i64s(p *int64) string {
	if p == nil {
		return "null"
	}
	return itoa64(*p)
}
func itoa64(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Validate normalises what it can and rejects what must be right. It never
// returns the offending value in the error, so an error can be logged.
func (e *Event) Validate() error {
	e.Provider = strings.ToLower(strings.TrimSpace(e.Provider))
	if !providerRe.MatchString(e.Provider) {
		return errf("provider")
	}
	if !modelRe.MatchString(e.RequestedModel) {
		return errf("requested_model")
	}
	if e.ReturnedModel != nil && !modelRe.MatchString(*e.ReturnedModel) {
		e.ReturnedModel = nil
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
		return errf("occurred_at")
	}
	if !installRe.MatchString(e.InstallID) {
		return errf("install_id")
	}
	if e.ServedHost == "" || (e.ServedHost != "unknown" && !providerRe.MatchString(e.ServedHost)) {
		e.ServedHost, e.HostEvidence = "unknown", "none"
	}
	if e.ProviderRequestID != nil && !idRe.MatchString(*e.ProviderRequestID) {
		e.ProviderRequestID = nil
	}
	if e.Gateway != nil && !providerRe.MatchString(*e.Gateway) {
		e.Gateway = nil
	}
	if e.ServiceTier != nil && !idRe.MatchString(*e.ServiceTier) {
		e.ServiceTier = nil
	}
	for _, p := range []*int64{e.InputTokens, e.CachedTokens, e.CacheWriteTokens, e.CacheWrite1hTokens, e.OutputTokens, e.ReasoningTokens} {
		if p != nil && *p < 0 {
			return errf("tokens")
		}
	}
	if e.CostUSD != nil && (*e.CostUSD < 0 || *e.CostUSD > 1e6) {
		e.CostUSD = nil
	}
	if e.CostUSD == nil {
		e.AmountBasis = BasisNone
	} else if e.AmountBasis == "" {
		e.AmountBasis = BasisInferred
	}
	if e.Attempt < 1 {
		e.Attempt = 1
	}
	if e.Calls < 1 {
		e.Calls = 1
	}
	if e.Calls > 10_000_000 {
		return errf("calls")
	}
	if e.Schema == 0 {
		e.Schema = SchemaVersion
	}
	if e.RecorderVersion == "" {
		e.RecorderVersion = RecorderVersion
	}
	if e.EventID == "" {
		e.EventID = EventID(e.Provider, e.ProviderRequestID, e.Attempt)
	}
	if e.RecordedAt == "" {
		e.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if e.StatusClass == "" {
		e.StatusClass = ClassOf(e.Status, e.Cancelled)
	}
	return nil
}

// ClassOf maps an HTTP status to the status class.
func ClassOf(status *int, cancelled bool) string {
	switch {
	case cancelled:
		return "cancelled"
	case status == nil:
		return "other"
	case *status >= 200 && *status < 300:
		return "ok"
	case *status == 401 || *status == 403:
		return "auth"
	case *status == 429:
		return "rate_limit"
	case *status == 404:
		return "invalid_model"
	case *status == 408 || *status == 504:
		return "timeout"
	case *status >= 500:
		return "server"
	default:
		return "other"
	}
}

type fieldError struct{ f string }

func (e fieldError) Error() string { return "invalid " + e.f }
func errf(f string) error          { return fieldError{f} }

// Ptr helpers.
func I64(v int64) *int64   { return &v }
func I(v int) *int         { return &v }
func F(v float64) *float64 { return &v }
func S(v string) *string   { return &v }
func B(v bool) *bool       { return &v }
