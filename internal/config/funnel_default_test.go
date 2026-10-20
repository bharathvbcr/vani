package config

import "testing"

func TestDefaultMinDescriptionRunesIsForty(t *testing.T) {
	if DefaultMinDescriptionRunes != 40 || (Funnel{}).DescriptionMin() != 40 {
		t.Fatalf("default %d, want 40 (a production threshold is not tuned to a test fixture)", DefaultMinDescriptionRunes)
	}
}
