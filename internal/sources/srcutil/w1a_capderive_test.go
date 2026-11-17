package srcutil

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w1a: the field caps are derived from the adapters' bounds (see storable.go),
// so each must hold the largest legitimate adapter output. The largest place
// count any adapter allows is 50 (ibm maxCities, talentbrew maxPlaces), and the
// adapters' own tests assert their maximum passes Storable.
func TestW1aCapsHoldTheLargestLegitimateAdapterOutput(t *testing.T) {
	const largestPlaceCount = 50
	const sep = len("; ")
	if need := largestPlaceCount*RealisticPlaceBytes + (largestPlaceCount-1)*sep; MaxLocation < need {
		t.Errorf("MaxLocation %d is under %d places of %d bytes (%d)", MaxLocation, largestPlaceCount, RealisticPlaceBytes, need)
	}
	if need := 6 * RealisticPlaceBytes; MaxDepartment < need {
		t.Errorf("MaxDepartment %d is under six elements of %d bytes (%d)", MaxDepartment, RealisticPlaceBytes, need)
	}
	if MaxCompany < RealisticPlaceBytes {
		t.Errorf("MaxCompany %d is under one element of %d bytes", MaxCompany, RealisticPlaceBytes)
	}
	if MaxKeyPart < RealisticPlaceBytes {
		t.Errorf("MaxKeyPart %d is under one element of %d bytes", MaxKeyPart, RealisticPlaceBytes)
	}
	// A workday or oraclehcm key has three parts after the prefix.
	if need := len("wd:") + 3*MaxKeyPart + 2; MaxKey < need {
		t.Errorf("MaxKey %d is under a three-part key of parts at MaxKeyPart (%d)", MaxKey, need)
	}
}

// w1a: a row with each capped field at its cap and the key at its widest
// legitimate shape is stored whole.
func TestW1aRowAtEveryCapIsStorable(t *testing.T) {
	p := storableRow()
	p.Key = posting.Key("wd:" + strings.Repeat("a", MaxKeyPart) + ":" + strings.Repeat("b", MaxKeyPart) + ":" + strings.Repeat("c", MaxKeyPart))
	p.Company = strings.Repeat("c", MaxCompany)
	p.Location = strings.Repeat("l", MaxLocation)
	p.Department = strings.Repeat("d", MaxDepartment)
	got, err := Storable(storableBoard(), p)
	if err != nil {
		t.Fatalf("a row at its caps was refused: %v", err)
	}
	if got.Location != p.Location || got.Company != p.Company || got.Department != p.Department || got.Key != p.Key {
		t.Error("a field changed")
	}
}

// w1a: the sum of the caps, each byte at JSON's worst (six), stays under the
// ledger's line limit, so the caps alone cannot make a line the ledger refuses.
func TestW1aCapsBudgetStaysUnderTheLedgerLine(t *testing.T) {
	sum := MaxCompany + MaxLocation + MaxDepartment + 2*MaxURL + MaxTitle + 2*MaxKey + MaxDescription
	if worst := 6 * sum; worst >= ledger.MaxLineBytes {
		t.Errorf("worst-case escaped budget %d is not under the ledger's %d", worst, ledger.MaxLineBytes)
	} else {
		t.Logf("worst-case escaped budget: %d of %d bytes", worst, ledger.MaxLineBytes)
	}
}
