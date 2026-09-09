package event

// Quota is one harness's rate-limit meter as last seen: the share of a
// window used and when it resets. Codex writes it into its session files;
// Anthropic sends it on every response, which the proxy sees. It is local
// state for the usage page and is never uploaded.
type Quota struct {
	Harness    string  `json:"harness"` // claude-code | codex
	Window     string  `json:"window"`  // 5h | 7d | overage | <N>m
	UsedPct    float64 `json:"used_pct"`
	ResetsAt   string  `json:"resets_at"` // RFC3339, "" when unknown
	Status     string  `json:"status"`    // allowed | rejected | ""
	Plan       string  `json:"plan"`      // codex: plus | pro | ...
	ObservedAt string  `json:"observed_at"`
}
