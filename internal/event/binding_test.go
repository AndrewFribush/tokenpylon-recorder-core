package event

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBindingNeedsOriginalIdentityEvidence(t *testing.T) {
	c := &CapturedBinding{BindingID: "a", OwnerRef: "owner", APIBase: "https://example.com", Generation: 1}
	if MatchCapture(c, "a", nil, time.Now()) != nil || MatchCapture(c, "a", &BillingIdentity{ProviderFingerprint: "b", Evidence: "session"}, time.Now()) != nil {
		t.Fatal("current login cannot assign history")
	}
	if MatchCapture(c, "a", &BillingIdentity{ProviderFingerprint: "a", Evidence: "session"}, time.Now()) == nil {
		t.Fatal("matching source evidence should bind")
	}
	b, _ := json.Marshal(&Event{Capture: c})
	if strings.Contains(string(b), "binding_id") || strings.Contains(string(b), "owner_ref") {
		t.Fatal("local metadata leaked into event wire schema")
	}
}
