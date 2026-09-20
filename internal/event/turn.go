package event

import "time"

// TurnState is observed lifecycle evidence, not a task lease or an idle-work
// declaration. A completed turn may resume when its user supplies more work.
type TurnState struct {
	At        time.Time
	Completed bool
}

func LatestCodexTurns(marks []Mark, now time.Time) map[string]TurnState {
	out := map[string]TurnState{}
	for _, m := range marks {
		if m.Harness != "codex" || m.Session == "" {
			continue
		}
		if m.Kind != "task_started" && m.Kind != "task_complete" && m.Kind != "turn_aborted" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, m.At)
		if err != nil || at.After(now) {
			continue
		}
		previous, exists := out[m.Session]
		if exists && (at.Before(previous.At) || (at.Equal(previous.At) && !previous.Completed)) {
			continue
		}
		out[m.Session] = TurnState{At: at, Completed: m.Kind != "task_started"}
	}
	return out
}
