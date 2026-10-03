package event

import (
	"testing"
	"time"
)

func TestStatusLineQuotasReadsEachWindowAndSkipsTheRest(t *testing.T) {
	now := time.Date(2026, 9, 23, 7, 0, 0, 0, time.UTC)
	raw := []byte(`{"five_hour":{"used_percentage":23.5,"resets_at":1738425600},"seven_day":{"used_percentage":41.2,"resets_at":1738857600},"spend_limit":{"used_percentage":162.8},"other":{"used_percentage":5}}`)
	qs := StatusLineQuotas(raw, now)
	if len(qs) != 3 {
		t.Fatalf("got %d readings: %+v", len(qs), qs)
	}
	if qs[0].Window != "5h" || qs[0].UsedPct != 23.5 || qs[0].ResetsAt != "2025-02-01T16:00:00Z" || qs[0].Harness != "claude-code" || qs[0].Scope != "" || qs[0].Note != "status line" || qs[0].ObservedAt != "2026-09-23T07:00:00Z" {
		t.Fatalf("5h: %+v", qs[0])
	}
	if qs[1].Window != "7d" || qs[1].UsedPct != 41.2 || qs[2].Window != "spend" || qs[2].UsedPct != 162.8 || qs[2].ResetsAt != "" {
		t.Fatalf("7d/spend: %+v %+v", qs[1], qs[2])
	}
	for _, raw := range []string{``, `null`, `{}`, `[1]`, `{"five_hour":{"resets_at":1}}`, `{"five_hour":{"used_percentage":-1}}`} {
		if got := StatusLineQuotas([]byte(raw), now); len(got) != 0 {
			t.Fatalf("%q produced %+v", raw, got)
		}
	}
}
