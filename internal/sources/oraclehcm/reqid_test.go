package oraclehcm

import "testing"

// A requisition id is digits (JPMorgan Chase) or digits after a short
// upper-case prefix (Dell's "R286084", 2026-11-17); a deleted one is the
// tombstone form. Anything else fails the listing.
func TestRequisitionIDShapes(t *testing.T) {
	for _, id := range []string{"210791916", "R286084", "REQ1", "ABCD12"} {
		if !idRE.MatchString(id) {
			t.Errorf("%q refused", id)
		}
	}
	for _, id := range []string{"", "R", "r286084", "ABCDE1", "R-286084", "R286084x", "../1", "1 2", "R286084\n",
		"123456789012345678901"} {
		if idRE.MatchString(id) {
			t.Errorf("%q accepted", id)
		}
	}
	if !tombstoneRE.MatchString("210260943_300015306973757_ORA_DELETED") {
		t.Error("tombstone refused")
	}
}
