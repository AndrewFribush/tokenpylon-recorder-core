package event

// Context is what a harness knows about a call beyond the call itself:
// which session it belongs to, which agent made it, which project it was
// working in. It stays on the user's machine (paths and branch names are
// theirs); the page joins it to events by id. Explicit planner participation
// can share project basenames and hashed session identities, never this object.
type Context struct {
	EventID    string           `json:"event_id"`
	Harness    string           `json:"harness"` // claude-code | codex
	Session    string           `json:"session"` // the harness's session or thread id
	Label      string           `json:"label"`   // a human name the harness gave the session, if any
	Agent      string           `json:"agent"`   // main | agent-<id> (a subagent) | sidechain
	Project    string           `json:"project"` // last path element of the working directory
	Cwd        string           `json:"cwd"`
	Branch     string           `json:"branch"`
	Process    string           `json:"process"` // cli | sdk | exec | ... as the harness names its entry point
	SourceKind string           `json:"source_kind,omitempty"`
	Billing    *BillingIdentity `json:"billing,omitempty"`
	Actions    []Action         `json:"actions,omitempty"`
}

// BillingIdentity is per-session provider evidence, not the current login.
// A history parser cannot infer it from the harness, root, project or model.
type BillingIdentity struct {
	Kind                string `json:"kind"`
	ProviderFingerprint string `json:"provider_fingerprint"`
	Evidence            string `json:"evidence"`
}

// Mark is a session event worth counting that is not a model call: a
// compaction, with the context size before and after.
type Mark struct {
	Harness string `json:"harness"`
	Session string `json:"session"`
	At      string `json:"at"`
	Kind    string `json:"kind"` // compaction
	Pre     int64  `json:"pre"`
	Post    int64  `json:"post"`
	Trigger string `json:"trigger"` // manual | auto
}

// Action is one tool block a call emitted: the tool's own name, a coarse
// kind, and a minimal target (a relative path, a command's leading words,
// a host, a subagent type). Never the arguments.
type Action struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}
