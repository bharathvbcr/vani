package kbsync

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRealBankCounts runs Load, the real rkb binary (claims export into a
// private temp dir) and Plan against the owner's real bank, and logs COUNTS
// ONLY: no block id, title or text is ever logged. It is opt-in
// (KBSYNC_REAL_BANK=1, with KBSYNC_RKB and KBSYNC_CODE_ROOT) and read-only: it
// never calls DevPrism and never writes under the bank's tree.
func TestRealBankCounts(t *testing.T) {
	if os.Getenv("KBSYNC_REAL_BANK") != "1" {
		t.Skip("set KBSYNC_REAL_BANK=1 (plus KBSYNC_BANK, KBSYNC_RKB, KBSYNC_CODE_ROOT) to run against the real bank")
	}
	bank, rkb, root := os.Getenv("KBSYNC_BANK"), os.Getenv("KBSYNC_RKB"), os.Getenv("KBSYNC_CODE_ROOT")
	if bank == "" || rkb == "" {
		t.Fatal("KBSYNC_BANK and KBSYNC_RKB are required")
	}
	b, err := Load(bank)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	claims, err := RKB{Binary: rkb, CodeRoot: root}.Claims(ctx)
	if err != nil {
		t.Fatalf("rkb claims: %v", err)
	}
	res := ResolveBank(b, claims)
	plan := Plan(res, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), time.Now())
	maxPayload, withTokens := 0, 0
	for _, it := range plan.Items {
		if len(it.Payload) > maxPayload {
			maxPayload = len(it.Payload)
		}
	}
	for _, bl := range b.Bullets {
		if tokenRe.MatchString(bl.Text) {
			withTokens++
		}
	}
	reasons := map[string]int{}
	for _, it := range plan.Items {
		if it.Action == ActionBlocked {
			reasons[it.Reason]++
		}
	}
	t.Logf("entries=%d bullets_in_bank=%d bullets_in_blocks=%d unattached=%d bullets_with_tokens=%d claims=%d unresolved_tokens=%d cautioned_claims=%d",
		len(b.Entries), len(b.Bullets), res.Bullets, res.Unattached, withTokens, len(claims.Values), res.Unresolved, len(res.Cautioned))
	t.Logf("plan(empty DevPrism): add=%d update=%d unchanged=%d blocked=%d max_payload_bytes=%d distinct_blocked_reasons=%d",
		plan.Count(ActionAdd), plan.Count(ActionUpdate), plan.Count(ActionUnchanged), plan.Count(ActionBlocked), maxPayload, len(reasons))
}
