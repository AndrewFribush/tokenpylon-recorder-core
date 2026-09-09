package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestQuotaFromHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.3")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1788942000")
	h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.64")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1788962400")
	h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Utilization", "0.0")
	h.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "five_hour")
	qs := QuotaFromHeaders(h, time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC))
	if len(qs) != 3 || qs[0].Window != "5h" || qs[0].UsedPct != 30 || qs[0].ResetsAt != "2026-09-09T08:20:00Z" || qs[0].Status != "allowed" || qs[1].UsedPct != 64 || qs[2].Window != "overage" || qs[2].ResetsAt != "" {
		t.Fatalf("%+v", qs)
	}
	if got := QuotaFromHeaders(http.Header{"X-Request-Id": {"r"}}, time.Now()); len(got) != 0 {
		t.Fatal("headers absent should give nothing")
	}
}
