package event

import (
	"strings"
	"testing"
	"time"
)

// The meters as seen on 2026-09-16: a session read 7d_oi at 86% as the
// account total. It is the Fable class weekly bucket, and the limit for
// Fable; every other model is bounded by the general 7d at 53%.
func TestQuotaAnnotateAndLimits(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 40, 0, 0, time.UTC)
	qs := []Quota{
		{Harness: "claude-code", Window: "5h", UsedPct: 5, ResetsAt: "2026-09-16T13:30:00Z", Scope: "claude-fable-5-1"},
		{Harness: "claude-code", Window: "7d", UsedPct: 53, ResetsAt: "2026-09-16T14:00:00Z", Scope: "claude-fable-5-1"},
		{Harness: "claude-code", Window: "7d_oi", UsedPct: 86, ResetsAt: "2026-09-16T14:00:00Z", Scope: "claude-fable-5-1", Status: "allowed_warning"},
		{Harness: "claude-code", Window: "overage", UsedPct: 0, ResetsAt: "2026-10-01T00:00:00Z", Scope: "claude-opus-5"},
		{Harness: "codex", Window: "5h", UsedPct: 99, ResetsAt: "2026-09-05T19:04:32Z", Plan: "plus"}, // ended
		{Harness: "codex", Window: "7d", UsedPct: 28, ResetsAt: "2026-09-19T13:36:37Z", Plan: "pro"},
	}
	a := Annotate(qs)
	if a[2].AppliesTo != "claude-fable-5-1 only" || !strings.Contains(a[2].Meaning, "model class only") || !strings.Contains(a[2].Meaning, "not of the account") {
		t.Fatalf("7d_oi: %q / %q", a[2].AppliesTo, a[2].Meaning)
	}
	if a[1].AppliesTo != "every model on this harness" || !strings.Contains(a[1].Meaning, "5h reset does not refill") {
		t.Fatalf("7d: %q / %q", a[1].AppliesTo, a[1].Meaning)
	}
	if qs[2].Meaning != "" {
		t.Fatal("Annotate must not touch its input")
	}
	ls := Limits(qs, now)
	want := map[string]Limit{}
	for _, l := range ls {
		want[l.Harness+"/"+l.Model] = l
	}
	f := want["claude-code/claude-fable-5-1"]
	if f.LimitedBy != "7d_oi" || f.UsedPct != 86 || f.HeadroomPct != 14 || strings.Join(f.Stacked, ",") != "7d_oi,7d,5h" {
		t.Fatalf("fable limit: %+v", f)
	}
	g := want["claude-code/"]
	if g.LimitedBy != "7d" || g.UsedPct != 53 || strings.Join(g.Stacked, ",") != "7d,5h" {
		t.Fatalf("general limit: %+v", g)
	}
	c := want["codex/"]
	if c.LimitedBy != "7d" || c.UsedPct != 28 || len(c.Stacked) != 1 {
		t.Fatalf("codex limit (ended 5h must be dropped): %+v", c)
	}
	if _, ok := want["claude-code/claude-opus-5"]; ok {
		t.Fatal("overage must not create a model line")
	}
	if !strings.Contains(QuotaHowToRead, "never a total") {
		t.Fatal("how-to-read lost its point")
	}
}
