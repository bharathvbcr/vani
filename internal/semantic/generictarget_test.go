package semantic

import "testing"

// An empty target summary still falls back to DefaultTarget (semantic matching
// is not silently off), and the profile says it did.
func TestProfileFromBankFlagsTheGenericDefaultTarget(t *testing.T) {
	for _, target := range []string{"", "   ", "\n\t"} {
		p := calProfile(t, target)
		if p.Target != DefaultTarget || !p.GenericTarget {
			t.Errorf("target %q: Target %q, GenericTarget %v; want the default and the flag", target, p.Target, p.GenericTarget)
		}
	}
	p := calProfile(t, "Platform and infrastructure engineering roles")
	if p.GenericTarget || p.Target == DefaultTarget {
		t.Errorf("a stated target reads as generic: %q (flag %v)", p.Target, p.GenericTarget)
	}
}
