package proxy

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
)

var bucketRe = regexp.MustCompile(`^anthropic-ratelimit-unified-([a-z0-9_]+)-utilization$`)

// QuotaFromHeaders reads Anthropic's subscription meters from a response.
// Every anthropic-ratelimit-unified-<bucket>-utilization header is a
// bucket (5h, 7d, overage, and model-class buckets such as 7d_oi that
// only appear on calls to the models they cover), with its -reset,
// -status and -surpassed-threshold companions. Nothing else on the
// response is read. The requested model is kept as the bucket's scope,
// since a bucket that shows up only for some models is about them.
func QuotaFromHeaders(hd http.Header, model string, now time.Time) []event.Quota {
	var out []event.Quota
	for name := range hd {
		m := bucketRe.FindStringSubmatch(strings.ToLower(name))
		if m == nil {
			continue
		}
		w := m[1]
		f, err := strconv.ParseFloat(hd.Get(name), 64)
		if err != nil || f < 0 {
			continue
		}
		pfx := "anthropic-ratelimit-unified-" + w
		q := event.Quota{Harness: "claude-code", Window: w, UsedPct: f * 100, Status: hd.Get(pfx + "-status"), Scope: model, ObservedAt: now.UTC().Format(time.RFC3339)}
		if hd.Get(pfx+"-surpassed-threshold") == "true" {
			q.Note = "surpassed threshold"
		}
		if sec, err := strconv.ParseInt(hd.Get(pfx+"-reset"), 10, 64); err == nil && sec > 0 {
			q.ResetsAt = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Window < out[j].Window })
	return out
}
