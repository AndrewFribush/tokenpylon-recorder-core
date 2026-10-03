package event

// SourceHealth is local-only status of one declared history directory.
// It is never included in Event or contribution uploads.
type SourceHealth struct {
	Harness        string `json:"harness"`
	Root           string `json:"root"`
	Status         string `json:"status"`
	ErrorCode      string `json:"error_code,omitempty"`
	AttemptedAt    string `json:"attempted_at"`
	LastSuccessAt  string `json:"last_success_at,omitempty"`
	Files          int    `json:"files"`
	Events         int    `json:"events"`
	SkippedRecords int    `json:"skipped_records"`
	// Detail names the first thing that went wrong in the last scan: the
	// reason and the file (relative to Root) or the root itself. Bounded;
	// never record content.
	Detail string `json:"detail,omitempty"`
}
