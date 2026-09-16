package event

import (
	"sort"
	"strings"
	"time"
)

// Quota is one harness's rate-limit meter as last seen: the share of a
// window used and when it resets. Codex writes it into its session files;
// Anthropic sends it on every response, which the proxy sees. It is local
// state for the usage page and is never uploaded.
type Quota struct {
	Harness    string  `json:"harness"` // claude-code | codex
	Window     string  `json:"window"`  // 5h | 7d | overage | <N>m
	UsedPct    float64 `json:"used_pct"`
	ResetsAt   string  `json:"resets_at"`       // RFC3339, "" when unknown
	Status     string  `json:"status"`          // allowed | rejected | ""
	Plan       string  `json:"plan"`            // codex: plus | pro | ...
	Scope      string  `json:"scope,omitempty"` // the model the bucket was last seen on; a bucket that only appears for some models is about them
	Note       string  `json:"note,omitempty"`  // e.g. "surpassed threshold"
	ObservedAt string  `json:"observed_at"`
	// Filled by Annotate for readers, never stored.
	AppliesTo string `json:"applies_to,omitempty"`
	Meaning   string `json:"meaning,omitempty"`
}

// QuotaHowToRead travels with every quotas answer. Several sessions read a
// model-class bucket (7d_oi) as the account total; it is not, and no bucket
// is: a call needs room in every bucket that applies to its model, and each
// percent is of that bucket alone.
const QuotaHowToRead = "Each used_pct is of that one bucket, never a total. A call goes through only if every bucket that applies to its model has room, so the tightest applicable bucket is the real limit (see limits). 5h buckets refill on their own schedule and never refill a weekly (7d*) bucket. A bucket named like 7d_oi is a weekly bucket for one model class only (scope names the model it was seen on) and sits on top of 5h and 7d."

// Annotate fills applies_to and meaning on each bucket so a reader can tell
// what it counts and what it stacks on. Pure; the stored rows are unchanged.
func Annotate(qs []Quota) []Quota {
	out := make([]Quota, len(qs))
	for i, q := range qs {
		q.AppliesTo, q.Meaning = describe(q)
		out[i] = q
	}
	return out
}

func describe(q Quota) (appliesTo, meaning string) {
	w := q.Window
	switch {
	case w == "overage":
		return "spend beyond the plan", "extra-usage spend, not a rate limit; 0% means none used"
	case w == "5h":
		return "every model on this harness", "rolling 5-hour bucket for all models; refills on its own schedule and does not refill any weekly bucket"
	case w == "7d":
		return "every model on this harness", "weekly bucket for all models; a 5h reset does not refill it"
	case strings.HasPrefix(w, "7d"):
		who := q.Scope
		if who == "" {
			who = strings.TrimLeft(strings.TrimPrefix(w, "7d"), "_@")
		}
		return who + " only", "weekly bucket for the " + who + " model class only, on top of 5h and 7d; a 5h reset does not refill it; its percent is of this bucket alone, not of the account"
	case strings.HasPrefix(w, "5h"):
		who := q.Scope
		if who == "" {
			who = strings.TrimLeft(strings.TrimPrefix(w, "5h"), "_@")
		}
		return who + " only", "rolling 5-hour bucket for " + who + " only, on top of the general 5h"
	default:
		return "every model on this harness", "rolling " + w + " bucket"
	}
}

// Limit is the tightest bucket that applies to one model on one harness:
// the number a session should plan against.
type Limit struct {
	Harness     string   `json:"harness"`
	Model       string   `json:"model"` // "" = every model without a bucket of its own
	LimitedBy   string   `json:"limited_by"`
	UsedPct     float64  `json:"used_pct"`
	HeadroomPct float64  `json:"headroom_pct"`
	ResetsAt    string   `json:"resets_at"`
	Stacked     []string `json:"stacked"` // every bucket that applies, tightest first
}

// Limits folds the buckets into one line per harness and model: the general
// buckets apply to every model, a class bucket only to the model it was seen
// on, and the tightest of those is the limit. Buckets whose window has
// ended and the overage pool are left out.
func Limits(qs []Quota, now time.Time) []Limit {
	live := func(q Quota) bool {
		if q.Window == "overage" {
			return false
		}
		if q.ResetsAt == "" {
			return true
		}
		t, err := time.Parse(time.RFC3339, q.ResetsAt)
		return err != nil || t.After(now)
	}
	general := func(q Quota) bool {
		return q.Window == "5h" || q.Window == "7d" || !strings.HasPrefix(q.Window, "5h") && !strings.HasPrefix(q.Window, "7d")
	}
	byHarness := map[string][]Quota{}
	var order []string
	for _, q := range qs {
		if !live(q) {
			continue
		}
		if _, ok := byHarness[q.Harness]; !ok {
			order = append(order, q.Harness)
		}
		byHarness[q.Harness] = append(byHarness[q.Harness], q)
	}
	var out []Limit
	for _, h := range order {
		buckets := byHarness[h]
		var models []string
		seen := map[string]bool{}
		for _, q := range buckets {
			if !general(q) && q.Scope != "" && !seen[q.Scope] {
				seen[q.Scope] = true
				models = append(models, q.Scope)
			}
		}
		models = append(models, "") // the rest: general buckets only
		for _, m := range models {
			var applies []Quota
			for _, q := range buckets {
				if general(q) || (m != "" && q.Scope == m) {
					applies = append(applies, q)
				}
			}
			if len(applies) == 0 {
				continue
			}
			sort.SliceStable(applies, func(i, j int) bool { return applies[i].UsedPct > applies[j].UsedPct })
			l := Limit{Harness: h, Model: m, LimitedBy: applies[0].Window, UsedPct: applies[0].UsedPct, HeadroomPct: 100 - applies[0].UsedPct, ResetsAt: applies[0].ResetsAt}
			for _, q := range applies {
				l.Stacked = append(l.Stacked, q.Window)
			}
			out = append(out, l)
		}
	}
	return out
}
