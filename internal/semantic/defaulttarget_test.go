package semantic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DefaultTarget must stay the comparison the decision model makes when no
// target is stated, or the two would disagree about what "fit" means.
func TestDefaultTargetMatchesTriageFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "decide", "jdtriage", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "compare it with "+DefaultTarget) {
		t.Fatalf("the jdtriage fixture no longer compares with %q when no target is stated; update DefaultTarget", DefaultTarget)
	}
}
