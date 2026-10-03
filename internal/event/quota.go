package event

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Quota is one harness's rate-limit meter as last seen: the share of a
// window used and when it resets. Codex writes it into its session files;
// Anthropic sends it on every response, which the proxy sees. It is local
// state for the usage page. Explicit planner participation can share selected
// meter metadata separately from ordinary usage-event uploads.
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
	// Filled by Annotate for readers, never stored. Pace compares use with
	// the share of the window gone: "over" means the bucket runs out before
	// its reset at the current rate, "under" means it does not.
	AppliesTo    string  `json:"applies_to,omitempty"`
	Meaning      string  `json:"meaning,omitempty"`
	ElapsedPct   float64 `json:"elapsed_pct,omitempty"`   // share of the window gone
	ProjectedPct float64 `json:"projected_pct,omitempty"` // where the meter lands at the reset at this rate
	Pace         string  `json:"pace,omitempty"`          // over | under | ""
	RunsOutAt    string  `json:"runs_out_at,omitempty"`   // RFC3339, when over pace
}

// WindowMinutes: 5h buckets (5h, 5h@premium) are 300 minutes, weekly ones
// (7d, 7d_oi) 10080, "<N>m" N; 0 when unknown.
func WindowMinutes(window string) int {
	switch {
	case strings.HasPrefix(window, "5h"):
		return 300
	case strings.HasPrefix(window, "7d"):
		return 10080
	case strings.HasSuffix(window, "m"):
		n, err := strconv.Atoi(strings.TrimSuffix(window, "m"))
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// Pace says how far through its window a bucket is and where the meter
// lands at the reset if use continues at the same rate. ok is false when
// the window is unknown, ended, or less than 5% gone (too early to say).
func Pace(q Quota, now time.Time) (elapsedPct, projectedPct float64, runsOutAt time.Time, ok bool) {
	mins := WindowMinutes(q.Window)
	if mins == 0 || q.ResetsAt == "" {
		return 0, 0, time.Time{}, false
	}
	reset, err := time.Parse(time.RFC3339, q.ResetsAt)
	if err != nil || !reset.After(now) {
		return 0, 0, time.Time{}, false
	}
	window := time.Duration(mins) * time.Minute
	left := reset.Sub(now)
	if left > window {
		left = window
	}
	elapsed := window - left
	elapsedPct = float64(elapsed) / float64(window) * 100
	if elapsedPct < 5 {
		return elapsedPct, 0, time.Time{}, false
	}
	projectedPct = q.UsedPct / elapsedPct * 100
	if projectedPct >= 100 && q.UsedPct > 0 {
		runsOutAt = now.Add(time.Duration(float64(elapsed) * (100 - q.UsedPct) / q.UsedPct))
	}
	return elapsedPct, projectedPct, runsOutAt, true
}

// QuotaHowToRead travels with every quotas answer. Several sessions read a
// model-class bucket (7d_oi) as the account total; it is not, and no bucket
// is: a call needs room in every bucket that applies to its model, and each
// percent is of that bucket alone.
const QuotaHowToRead = "Each used_pct is of that one bucket, never a total. pace says whether a bucket runs out before its reset at the current rate (over) or not (under), from elapsed_pct, the share of the window gone. A call goes through only if every bucket that applies to its model has room, so the tightest applicable bucket is the real limit (see limits). 5h buckets refill on their own schedule and never refill a weekly (7d*) bucket. A bucket named like 7d_oi is a weekly bucket for one model class only (scope names the model it was seen on) and sits on top of 5h and 7d."

// Annotate fills applies_to and meaning on each bucket so a reader can tell
// what it counts and what it stacks on. Pure; the stored rows are unchanged.
func Annotate(qs []Quota, now time.Time) []Quota {
	out := make([]Quota, len(qs))
	for i, q := range qs {
		q.AppliesTo, q.Meaning = describe(q)
		if el, proj, at, ok := Pace(q, now); ok {
			q.ElapsedPct, q.ProjectedPct = math.Round(el), math.Round(proj)
			q.Pace = "under"
			if proj >= 100 {
				q.Pace = "over"
				q.RunsOutAt = at.UTC().Format(time.RFC3339)
			}
		}
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
	Pace        string   `json:"pace,omitempty"` // over | under: does the limit run out before its reset at this rate
	RunsOutAt   string   `json:"runs_out_at,omitempty"`
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
			if a := Annotate(applies[:1], now)[0]; a.Pace != "" {
				l.Pace, l.RunsOutAt = a.Pace, a.RunsOutAt
			}
			for _, q := range applies {
				l.Stacked = append(l.Stacked, q.Window)
			}
			out = append(out, l)
		}
	}
	return out
}

// StatusLineQuotas reads the rate_limits object Claude Code passes to a
// status line command (five_hour, seven_day and spend_limit, each with
// used_percentage 0..100 and resets_at in Unix seconds) into meter readings
// for the claude-code harness. It is the meter source when Claude Code talks
// to Anthropic directly instead of through the proxy: the same windows as the
// proxy's unified headers, but account-wide (no model scope) and only as fresh
// as the last response the status line was told about. Absent or malformed
// windows are skipped; nothing is invented.
func StatusLineQuotas(raw []byte, now time.Time) []Quota {
	var rl map[string]struct {
		UsedPct  *float64 `json:"used_percentage"`
		ResetsAt *int64   `json:"resets_at"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &rl) != nil {
		return nil
	}
	windows := map[string]string{"five_hour": "5h", "seven_day": "7d", "spend_limit": "spend"}
	var out []Quota
	for _, key := range []string{"five_hour", "seven_day", "spend_limit"} {
		v, ok := rl[key]
		if !ok || v.UsedPct == nil || *v.UsedPct < 0 || *v.UsedPct > 10000 {
			continue
		}
		q := Quota{Harness: "claude-code", Window: windows[key], UsedPct: *v.UsedPct, Note: "status line", ObservedAt: now.UTC().Format(time.RFC3339)}
		if v.ResetsAt != nil && *v.ResetsAt > 0 {
			q.ResetsAt = time.Unix(*v.ResetsAt, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, q)
	}
	return out
}
