package main

import (
	"fmt"
	"strings"
)

// Verdict is a recorded judgement on a figure or a statement: a validator
// report line, a first-pass reading or a blind pass. It is a string on the
// wire (the verify log and claims.json keep their bytes); the constants name
// the values rkb reads. A value outside them is carried as written and is
// never CONFIRMED.
type Verdict string

const (
	// validator report verdicts
	VerdictPrimary  Verdict = "PRIMARY"
	VerdictLedger   Verdict = "LEDGER"
	VerdictSkip     Verdict = "SKIP"
	VerdictUnbacked Verdict = "UNBACKED"
	VerdictFail     Verdict = "FAIL"
	// first pass 1 read by a person from a source file
	VerdictManual Verdict = "MANUAL"
	// recorded passes
	VerdictConfirmed   Verdict = "CONFIRMED"
	VerdictScope       Verdict = "SCOPE"
	VerdictMismatch    Verdict = "MISMATCH"
	VerdictUnsupported Verdict = "UNSUPPORTED"
)

func (v Verdict) String() string { return string(v) }

// validatorRank orders validator verdicts from best to worst; parseVM keeps
// the worst per check. An unknown verdict ranks below PRIMARY.
func (v Verdict) validatorRank() int {
	switch v {
	case VerdictPrimary:
		return 0
	case VerdictLedger:
		return 1
	case VerdictSkip:
		return 2
	case VerdictUnbacked:
		return 3
	case VerdictFail:
		return 4
	}
	return -1
}

// BacksFigure: the validator verdicts that satisfy pass 1 by themselves.
func (v Verdict) BacksFigure() bool { return v == VerdictPrimary || v == VerdictLedger }

// ContradictsFigure: the validator verdicts that void pass 1 whatever else
// was logged.
func (v Verdict) ContradictsFigure() bool { return v == VerdictFail || v == VerdictUnbacked }

// RefutesFigure: blind-pass verdicts that say the sentence's wording or value
// is wrong (as opposed to a pass that is missing, stale or badly filled in).
func (v Verdict) RefutesFigure() bool { return v == VerdictScope || v == VerdictMismatch }

// independentVerifier says whether a verifier name can stand for a blind
// pass: it must name itself and not be the author's own seat.
func independentVerifier(by string) bool {
	switch strings.ToLower(strings.TrimSpace(by)) {
	case "", "author", "drafter", "me", "self", "user":
		return false
	}
	return true
}

// Report is the one assessment of a figure: the state export records, status
// prints and the worksheet filters by. Nothing outside Assess decides whether
// a figure is verified.
type Report struct {
	P1        bool
	P1Verdict Verdict // PRIMARY, LEDGER, MANUAL; empty when P1 is false
	P2OK      bool    // every sentence that uses the figure holds a fresh CONFIRMED blind pass
	Confirmed []string
	Problems  []string
	Refuted   map[string]string // sentence hash -> verdict: evidence
	// Independent: at least one blind pass was read and every one read names
	// a verifier that is not the author's own seat. Recorded, not gated on:
	// claims ingest refuses such a verifier, so only a hand-edited log can
	// make it false.
	Independent bool
}

// DoubleVerified: both passes hold.
func (r Report) DoubleVerified() bool { return r.P1 && r.P2OK }

// Confirms: the sentence with this hash holds a fresh CONFIRMED blind pass.
func (r Report) Confirms(sentenceHash string) bool { return contains(r.Confirmed, sentenceHash) }

// Assess judges one figure against the verify log and the validator report,
// for the sentences that use it.
func Assess(cl *Claim, log VerifyLog, vm map[string]vmResult, sentences []string) Report {
	var r Report
	entries := log[cl.ID]
	var current []LogEntry
	voided := 0
	for _, e := range entries {
		if e.Value == cl.Value {
			current = append(current, e)
		} else {
			voided++
		}
	}
	vmr, hasVM := vm[cl.Check]
	if hasVM && vmr.Verdict.BacksFigure() {
		r.P1, r.P1Verdict = true, vmr.Verdict
	}
	if !r.P1 {
		var stale []string
		for _, e := range current {
			if e.Pass == 1 && e.Verdict == VerdictConfirmed && e.Found != "" && e.SourcePath != "" && !circularSource(e.SourcePath) {
				if why := manualSourceProblem(e); why != "" {
					stale = append(stale, fmt.Sprintf("%s: manual pass 1 refused, %s", cl.ID, why))
					continue
				}
				r.P1, r.P1Verdict = true, VerdictManual
			}
		}
		if !r.P1 {
			r.Problems = append(r.Problems, stale...)
		}
	}
	if hasVM && vmr.Verdict.ContradictsFigure() {
		r.P1 = false
		r.Problems = append(r.Problems, fmt.Sprintf("validator %s: %s", vmr.Verdict, truncate(vmr.Detail, 120)))
	}
	if !r.P1 {
		msg := "pass 1 missing"
		if hasVM {
			msg += " (validator " + string(vmr.Verdict) + ")"
		}
		r.Problems = append(r.Problems, msg)
	}
	r.P2OK = len(sentences) > 0
	read, allIndependent := 0, true
	for _, s := range sentences {
		h := sentenceHash(s, cl.ID)
		var latest *LogEntry
		for i := range current {
			e := &current[i]
			if e.Pass == 2 && e.SentenceHash == h {
				latest = e
			}
		}
		if latest != nil {
			read++
			allIndependent = allIndependent && independentVerifier(latest.By)
		}
		switch {
		case latest == nil:
			r.P2OK = false
			r.Problems = append(r.Problems, "pass 2 missing for: "+truncate(maskedSentence(s, cl.ID), 70))
		case latest.Verdict != VerdictConfirmed:
			r.P2OK = false
			r.Problems = append(r.Problems, fmt.Sprintf("latest blind pass %s: %s", latest.Verdict, truncate(latest.Evidence, 110)))
			if latest.Verdict.RefutesFigure() {
				if r.Refuted == nil {
					r.Refuted = map[string]string{}
				}
				r.Refuted[h] = string(latest.Verdict) + ": " + truncate(latest.Evidence, 200)
			}
		case circularSource(latest.SourcePath):
			r.P2OK = false
			r.Problems = append(r.Problems, "pass 2 cited a claims/resume/KB file, not an artifact: "+latest.SourcePath)
		default:
			sha := sha256File(inRoot(latest.SourcePath))
			if sha == "" && isUnmountedSibling(codeRoot(), latest.SourcePath) && latest.SourceSHA != "" {
				sha = latest.SourceSHA
			}
			if sha == "" || (latest.SourceSHA != "" && sha != latest.SourceSHA) {
				r.P2OK = false
				r.Problems = append(r.Problems, "pass 2 source changed or deleted since it was read: "+latest.SourcePath)
			} else {
				r.Confirmed = append(r.Confirmed, h)
			}
		}
	}
	r.Independent = read > 0 && allIndependent
	if voided > 0 {
		r.Problems = append(r.Problems, fmt.Sprintf("%d logged pass(es) void: value changed since", voided))
	}
	return r
}
