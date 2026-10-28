package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestDeriveBoardWorkdayThreeDigitPod: a listing page on a tenant host whose
// pod number has three digits (Postman's is wd108) is that tenant's Workday
// board, the same board config.ValidBoard accepts, not a careers page.
func TestDeriveBoardWorkdayThreeDigitPod(t *testing.T) {
	b, res := deriveBoard("https://postman.wd108.myworkdayjobs.com/en-US/careers", nil, "Postman", nil)
	if res.Status != "" || b.ATS != posting.Workday || b.Token != "postman/careers" || b.Host != "postman.wd108.myworkdayjobs.com" {
		t.Fatalf("board %+v, result %+v", b, res)
	}
	if err := config.ValidBoard(b); err != nil {
		t.Fatalf("derived board is not valid: %v", err)
	}
	if b, _ := deriveBoard("https://postman.wd1080.myworkdayjobs.com/en-US/careers", nil, "Postman", nil); b.ATS == posting.Workday {
		t.Errorf("four-digit pod read as Workday: %+v", b)
	}
}
