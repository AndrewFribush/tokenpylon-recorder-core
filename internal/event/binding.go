package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// CapturedBinding is local-only. Only binding_id, generation and captured_at
// enter the upload envelope. Owner and endpoint fences never enter event JSON.
type CapturedBinding struct {
	BindingID             string `json:"binding_id,omitempty"`
	Generation            int    `json:"generation,omitempty"`
	CapturedAt            string `json:"captured_at"`
	OwnerRef              string `json:"owner_ref,omitempty"`
	APIBase               string `json:"api_base"`
	CredentialFingerprint string `json:"credential_fingerprint,omitempty"`
}

func (c *CapturedBinding) Key() string {
	if c == nil {
		return ""
	}
	owner := c.OwnerRef
	if owner == "" {
		owner = c.CredentialFingerprint
	}
	b, _ := json.Marshal([]any{c.APIBase, owner, c.BindingID, c.Generation})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (e *Event) StorageID() string {
	if e.Capture == nil {
		return e.EventID
	}
	return e.EventID + ":" + e.Capture.Key()
}

// MatchCapture needs identity from the original source record, never current login alone.
func MatchCapture(binding *CapturedBinding, expected string, billing *BillingIdentity, now time.Time) *CapturedBinding {
	if binding == nil || billing == nil || expected == "" || billing.ProviderFingerprint != expected || billing.Evidence == "" {
		return nil
	}
	copy := *binding
	copy.CapturedAt = now.UTC().Format(time.RFC3339Nano)
	return &copy
}
