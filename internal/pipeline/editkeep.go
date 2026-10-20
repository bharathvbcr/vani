package pipeline

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
	"github.com/bharathvbcr/Vani/internal/textsim"
)

const (
	// EditKeepJaccard is the 5-word-shingle overlap an edited description needs
	// with the stored one for its triage to be kept.
	EditKeepJaccard = 0.90
	// EditKeepMaxBytes caps the board text read on either side; a longer text
	// is re-asked rather than compared on a prefix.
	EditKeepMaxBytes = 64 << 10
	// EditKeepMaxEdits bounds consecutive kept edits: each is compared with the
	// previous version only (the ledger holds no older text), so small edits
	// could otherwise drift without end. The next edit re-asks, which forgets
	// the count. The basis text is not stored, so this counter is the whole bound.
	EditKeepMaxEdits = 3
)

var (
	// h1bForms folds "h-1b", "h1-b" and "h-1-b" to "h1b" before tokenizing.
	h1bForms = regexp.MustCompile(`\bh-?1-?b\b`)
	onsite   = regexp.MustCompile(`\bon-site\b`)
)

// sensitiveStems are the words a reader's decision can turn on: work
// authorization, work mode and seniority. A shingle holding one is a sentence
// that may say "we sponsor" or "we do not", "remote" or "onsite", "3+ years" or
// "8+ years", so a changed such shingle always re-asks, however small the edit.
var sensitiveStems = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		visa sponsor sponsorship sponsoring h1b opt cpt clearance cleared citizen
		citizenship resident residency green card authorized authorization itar
		export polygraph ts sci
		remote hybrid onsite office relocate relocation relocating
		years yrs year senior sr staff principal intern internship graduate grad
		entry junior jr lead manager director head`) {
		m[w] = true
	}
	return m
}()

// KeepAcrossEdit decides whether an edit to a posting's description is small
// and safe enough for its triage to stand. It keeps only when all hold:
//
//   - both board texts (srcutil.SourceText) are within EditKeepMaxBytes and
//     hold NearDupMinWords words, so there is something to compare;
//   - their 5-word shingles overlap by at least EditKeepJaccard;
//   - the role title (roleTitle) and the places (looseLocation) are unchanged;
//   - no shingle holding a sensitive word was added, removed or altered.
//
// Anything else re-asks. score is the overlap, for the kept annotation.
func KeepAcrossEdit(old, cur posting.Posting) (score string, keep bool) {
	if roleTitle(old.Title) != roleTitle(cur.Title) || looseLocation(old) != looseLocation(cur) {
		return "", false
	}
	oldText, curText := srcutil.SourceText(old.Description), srcutil.SourceText(cur.Description)
	if len(oldText) > EditKeepMaxBytes || len(curText) > EditKeepMaxBytes {
		return "", false
	}
	ow, cw := editWords(oldText), editWords(curText)
	if len(ow) < NearDupMinWords || len(cw) < NearDupMinWords {
		return "", false
	}
	j := textsim.Jaccard(textsim.Shingles(ow, NearDupShingleWords), textsim.Shingles(cw, NearDupShingleWords))
	if j < EditKeepJaccard {
		return "", false
	}
	if !sameSet(sensitiveShingles(ow), sensitiveShingles(cw)) {
		return "", false
	}
	return scoreText(j), true
}

// keepAcrossEdit is KeepAcrossEdit for a stored record: there must be derived
// results to keep, and a Dead record (whose relisting restores or reopens it) is
// never kept.
func keepAcrossEdit(rec ledger.Record, cur posting.Posting) (string, bool) {
	if rec.Status == posting.Dead || len(staleAnnotations(rec.Annotations)) == 0 || keptEdits(rec) >= EditKeepMaxEdits {
		return "", false
	}
	return KeepAcrossEdit(rec.Posting, cur)
}

// keptEvent stores the edited posting and its new description hash and keeps
// the record's status and annotations, noting the overlap that justified it.
func keptEvent(rec ledger.Record, p posting.Posting, hash string, now time.Time, score string) ledger.Event {
	ann := keptAnnotations(rec, score)
	ann[DescHashKey] = hash
	return ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &p,
		Annotations: ann, Reason: "changed (kept)"}
}

// keptAnnotations are the notes a kept edit writes: the overlap with the
// previous version, the running count of kept edits and, once, the hash of the
// version the triage actually read (the record's current one at the first keep).
func keptAnnotations(rec ledger.Record, score string) map[string]string {
	basis := rec.Annotations[ledger.AnnTriageBasisHash]
	if basis == "" {
		basis = rec.Annotations[DescHashKey]
	}
	if basis == "" {
		basis = rec.Posting.DescriptionHash()
	}
	return map[string]string{
		ledger.AnnTriageKept:      score,
		ledger.AnnTriageKeptEdits: strconv.Itoa(keptEdits(rec) + 1),
		ledger.AnnTriageBasisHash: basis,
	}
}

func keptEdits(rec ledger.Record) int {
	n, err := strconv.Atoi(rec.Annotations[ledger.AnnTriageKeptEdits])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func editWords(s string) []string {
	s = strings.ToLower(s)
	s = h1bForms.ReplaceAllString(s, "h1b")
	s = onsite.ReplaceAllString(s, "onsite")
	return textsim.Words(s, EditKeepMaxBytes)
}

// sensitiveShingles is the set of 5-word windows that hold a sensitive word.
func sensitiveShingles(ws []string) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i+NearDupShingleWords <= len(ws); i++ {
		win := ws[i : i+NearDupShingleWords]
		for _, w := range win {
			if sensitiveStems[w] {
				out[strings.Join(win, " ")] = struct{}{}
				break
			}
		}
	}
	return out
}

func sameSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
