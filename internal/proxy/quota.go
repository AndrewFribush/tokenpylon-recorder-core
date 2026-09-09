package proxy

import (
	"net/http"
	"strconv"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
)

// QuotaFromHeaders reads Anthropic's subscription meters from a response:
// anthropic-ratelimit-unified-<window>-utilization (0..1), -reset (unix
// seconds) and -status, for the 5h and 7d windows and the overage pool.
// Nothing else on the response is read. Empty when the headers are absent
// (API-key traffic carries a different, per-key set).
func QuotaFromHeaders(hd http.Header, now time.Time) []event.Quota {
	var out []event.Quota
	for _, w := range []string{"5h", "7d", "overage"} {
		util := hd.Get("anthropic-ratelimit-unified-" + w + "-utilization")
		if util == "" {
			continue
		}
		f, err := strconv.ParseFloat(util, 64)
		if err != nil || f < 0 {
			continue
		}
		q := event.Quota{Harness: "claude-code", Window: w, UsedPct: f * 100, Status: hd.Get("anthropic-ratelimit-unified-" + w + "-status"), ObservedAt: now.UTC().Format(time.RFC3339)}
		if sec, err := strconv.ParseInt(hd.Get("anthropic-ratelimit-unified-"+w+"-reset"), 10, 64); err == nil && sec > 0 {
			q.ResetsAt = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, q)
	}
	return out
}
