package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// skillsLabel labels the chunks made of stack terms.
const skillsLabel = "skills"

// skillsPerChunk groups stack terms into chunks of this many.
const skillsPerChunk = 12

// Profile is the owner's knowledge base cut into chunks to embed. It holds
// only role titles, stack terms and bullet text. Org names, dates, locations
// and contact or identity fields are never read from the bank.
type Profile struct {
	Chunks []Chunk
	// Target is the owner's one-line target (role and domain). It is not a
	// chunk: it is the on-domain calibration anchor.
	Target string
	// Truncated is true when the bank held more than MaxProfileChunks chunks.
	Truncated bool
}

// ProfileFromBank builds a Profile. Order is deterministic: skills first, then
// each entry (sorted by id) as its title followed by its bullets, so a bank over
// the chunk cap loses the tail of the last entries, not the skills. Duplicate
// texts (compared ignoring case) are dropped; markdown, URLs, e-mail addresses
// and {{clm...}} claim tokens are removed from bullet text. An empty bank or an
// empty target is an error: the target is the calibration ceiling.
func ProfileFromBank(b kbsync.Bank, targetSummary string) (Profile, error) {
	target := truncBytes(tidy(kbsync.PlainText(targetSummary)), MaxProfileChunkBytes)
	if target == "" {
		return Profile{}, fmt.Errorf("%w: the target summary is empty", ErrEmpty)
	}
	p := Profile{Target: target}
	seen := map[string]bool{}
	add := func(label, text string) {
		text = truncBytes(text, MaxProfileChunkBytes)
		if text == "" || label == "" {
			return
		}
		k := strings.ToLower(text)
		if seen[k] {
			return
		}
		if len(p.Chunks) >= MaxProfileChunks {
			p.Truncated = true
			return
		}
		seen[k] = true
		p.Chunks = append(p.Chunks, Chunk{Label: label, Text: text, Weight: 1})
	}

	ids := b.EntryIDs()
	var skills []string
	skillSeen := map[string]bool{}
	for _, id := range ids {
		terms, err := kbsync.ParseStack(b.Entries[id].Stack)
		if err != nil {
			continue // an unreadable stack is skipped, not guessed at
		}
		for _, t := range terms {
			t = tidy(t)
			if k := strings.ToLower(t); t != "" && !skillSeen[k] {
				skillSeen[k] = true
				skills = append(skills, t)
			}
		}
	}
	for i := 0; i < len(skills); i += skillsPerChunk {
		j := min(i+skillsPerChunk, len(skills))
		add(skillsLabel, "Skills: "+strings.Join(skills[i:j], ", "))
	}

	for _, id := range ids {
		e := b.Entries[id]
		title := truncBytes(tidy(kbsync.PlainText(e.Title)), 160)
		label := title
		if label == "" {
			label = "experience"
		}
		add(label, title)
		for _, bid := range e.Bullets {
			bl, ok := b.Bullets[bid]
			if !ok {
				continue
			}
			if text := cleanBulletText(bl.Text); wordCount(text) >= 3 {
				add(label, text)
			}
		}
	}
	if len(p.Chunks) == 0 {
		return Profile{}, fmt.Errorf("%w: the bank yields no titles, skills or bullets", ErrEmpty)
	}
	return p, nil
}

// Hash identifies the profile content for cache keys: every chunk label and
// text, the target and Version. Equal profiles hash equal; any change differs.
func (p Profile) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "semantic/v%d\x00%d:%s\x00", Version, len(p.Target), p.Target)
	for _, c := range p.Chunks {
		fmt.Fprintf(h, "%d:%s\x00%d:%s\x00", len(c.Label), c.Label, len(c.Text), c.Text)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
