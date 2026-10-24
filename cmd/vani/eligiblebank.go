package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// eligibleKBTimeout bounds the knowledge-base read made where a caller holds no
// context of its own (ranking views, `vani kbmatch status`): rkb's claims
// export takes about 0.1 s, and kbsync bounds the subprocess itself.
const eligibleKBTimeout = 90 * time.Second

// eligibleBank is the one place a command reads the owner's knowledge base for
// anything that reaches a model, an embedding profile or an answer. The bank
// alone is not the owner's word on what is current: it holds superseded
// wordings, bullets whose figures Claims.tex has withdrawn and drafts nobody
// reviewed. The selections (kb.selections: the resumes in use) say what is, and
// kbsync.Eligible applies the one rule (internal/kbsync/eligible.go). It fails
// closed: with no selections or no rkb binary to resolve claims there is no
// eligible bullet, and nothing falls back to the whole bank. The error carries
// no config path beyond the key names.
func eligibleBank(ctx context.Context, cfg config.Config) (kbsync.Bank, kbsync.Claims, kbsync.Eligibility, error) {
	var none kbsync.Eligibility
	switch {
	case cfg.KB.Bank == "":
		return kbsync.Bank{}, kbsync.Claims{}, none, errors.New("kb.bank is not set")
	case len(cfg.KB.Selections) == 0:
		return kbsync.Bank{}, kbsync.Claims{}, none, errors.New("kb.selections is empty: list the rkb resumes you currently use (resumes/*.json); nothing is built from the whole bank")
	case cfg.KB.RKB == "":
		return kbsync.Bank{}, kbsync.Claims{}, none, errors.New("kb.rkb is not set: eligibility needs rkb's claim values and CAUTION flags")
	}
	b, err := kbsync.Load(cfg.KB.Bank)
	if err != nil {
		return kbsync.Bank{}, kbsync.Claims{}, none, err
	}
	sels, err := kbsync.LoadSelections(cfg.KB.Selections)
	if err != nil {
		return kbsync.Bank{}, kbsync.Claims{}, none, err
	}
	claims, err := kbsync.RKB{Binary: cfg.KB.RKB, CodeRoot: cfg.KB.CodeRoot, Runner: rkbRunner}.Claims(ctx)
	if err != nil {
		return kbsync.Bank{}, kbsync.Claims{}, none, err
	}
	return b, claims, kbsync.Eligible(b, claims, sels), nil
}

// eligibleSummary is the one-line account of what eligibleBank kept, for stderr.
func eligibleSummary(cfg config.Config, el kbsync.Eligibility) string {
	return fmt.Sprintf("%d eligible bullet(s) from %d selection(s); %d listed bullet(s) held back", len(el.Bullets), len(cfg.KB.Selections), len(el.Why))
}
