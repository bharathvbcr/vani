// Package kbsync syncs rkb's bank.json (the canonical career knowledge base)
// one way into DevPrism's career.db through DevPrism's MCP career tools.
//
// Sources of truth, cited so a drift is findable:
//
//   - bank.json schema: rkb/bank.go (Bank :160, BankEntry :124, Bullet :49);
//     {{clm...}} tokens: rkb/render.go:12; claim values: rkb/claims.go:497-530
//     (`rkb claims export`), detexed by rkb/numbers.go:53.
//   - DevPrism: src-tauri/src/mcp/tools_career.rs (career_upsert_block :840,
//     career_get_profile :808) and career_db/mod.rs (ExperienceBlock :82).
//
// Only the approved pool is synced (Eligible over kb.selections, pool.go). A
// plain sync never deletes: a DevPrism block rkb does not know is reported as
// orphaned and left alone; only `kb sync --replace` (replace.go) deletes
// orphans, after a verified backup (backup.go). The sync never auto-confirms
// DevPrism's overwrite confirmation without --confirm: that block is reported
// as needs_confirmation.
package kbsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Bounds. All are ceilings, not targets.
const (
	// MaxBankBytes caps bank.json (the real one is about 0.6 MiB).
	MaxBankBytes = 8 << 20
	// MaxIDLen caps an entry or bullet id.
	MaxIDLen = 128
)

// ErrBank wraps every bank.json problem.
var ErrBank = errors.New("kbsync: bank.json")

var idRe = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// Entry is the part of rkb's BankEntry this sync reads (rkb/bank.go:124).
// Stack becomes DevPrism skills (tags.go). Origin lists the entry ids rkb's
// importer derived this one from (rkb/importtex.go:345-365): an entry whose
// origin names another entry is a variant of it. Fields DevPrism cannot
// represent (group, layout, members, links, sub, lead) are ignored by design.
type Entry struct {
	Title    string   `json:"title"`
	Org      string   `json:"org"`
	Dates    string   `json:"dates"`
	Location string   `json:"location"`
	Stack    string   `json:"stack"`
	Origin   []string `json:"origin"`
	Bullets  []string `json:"bullets"`
}

// maxOrigins caps an entry's origin list.
const maxOrigins = 64

// Bullet is the part of rkb's Bullet this sync reads (rkb/bank.go:49). Text
// still carries {{clm...}} tokens until Resolve. CautionOK and Review feed
// Eligible (eligible.go).
type Bullet struct {
	Text      string              `json:"text"`
	Author    string              `json:"author"`
	CautionOK map[string]string   `json:"caution_ok"`
	Review    map[string][]Review `json:"review"`
}

// Review is one reader verdict rkb recorded on a bullet's wording (rkb/bank.go
// Review). TextHash is a prefix of sha1 over the resolved plain text.
type Review struct {
	Round    int    `json:"round"`
	Verdict  string `json:"verdict"`
	TextHash string `json:"text_hash"`
	By       string `json:"by"`
}

// Bank is the parsed bank.json: entries and bullets keyed by rkb id. Headers
// (name, contact) and lines are never read, so personal data cannot leak
// through this package.
type Bank struct {
	Entries map[string]Entry
	Bullets map[string]Bullet
}

// Load parses bank.json strictly: bounded size, one JSON object, no duplicate
// keys, typed fields, no null or id-less entries or bullets, no dangling
// bullet references (mirroring rkb's parseBank, bank.go:329-359). Unknown
// fields are tolerated because rkb itself preserves them.
func Load(bankPath string) (Bank, error) {
	f, err := os.Open(bankPath)
	if err != nil {
		return Bank{}, fmt.Errorf("%w: %v", ErrBank, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxBankBytes+1))
	if err != nil {
		return Bank{}, fmt.Errorf("%w: read: %v", ErrBank, err)
	}
	if len(raw) > MaxBankBytes {
		return Bank{}, fmt.Errorf("%w: larger than %d bytes", ErrBank, MaxBankBytes)
	}
	return Parse(raw)
}

// Parse is Load on bytes already read.
func Parse(raw []byte) (Bank, error) {
	if len(raw) > MaxBankBytes {
		return Bank{}, fmt.Errorf("%w: larger than %d bytes", ErrBank, MaxBankBytes)
	}
	if err := checkJSON(raw); err != nil {
		return Bank{}, fmt.Errorf("%w: %v", ErrBank, err)
	}
	var top struct {
		Entries map[string]*Entry  `json:"entries"`
		Bullets map[string]*Bullet `json:"bullets"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return Bank{}, fmt.Errorf("%w: %v", ErrBank, err)
	}
	b := Bank{Entries: map[string]Entry{}, Bullets: map[string]Bullet{}}
	for id, bl := range top.Bullets {
		if err := checkID("bullet", id); err != nil {
			return Bank{}, err
		}
		if bl == nil {
			return Bank{}, fmt.Errorf("%w: bullet %q is null", ErrBank, id)
		}
		b.Bullets[id] = *bl
	}
	for id, e := range top.Entries {
		if err := checkID("entry", id); err != nil {
			return Bank{}, err
		}
		if e == nil {
			return Bank{}, fmt.Errorf("%w: entry %q is null", ErrBank, id)
		}
		if len(e.Origin) > maxOrigins {
			return Bank{}, fmt.Errorf("%w: entry %q lists more than %d origins", ErrBank, id, maxOrigins)
		}
		for _, o := range e.Origin {
			if err := checkID("origin of an entry", o); err != nil {
				return Bank{}, err
			}
		}
		seen := map[string]bool{}
		for _, bid := range e.Bullets {
			if _, ok := b.Bullets[bid]; !ok {
				return Bank{}, fmt.Errorf("%w: entry %q references missing bullet %q", ErrBank, id, bid)
			}
			if seen[bid] {
				return Bank{}, fmt.Errorf("%w: entry %q lists bullet %q twice", ErrBank, id, bid)
			}
			seen[bid] = true
		}
		b.Entries[id] = *e
	}
	return b, nil
}

func checkID(kind, id string) error {
	if id == "" || len(id) > MaxIDLen || !idRe.MatchString(id) {
		return fmt.Errorf("%w: %s id %q is empty, longer than %d, or has characters outside [A-Za-z0-9._:-]", ErrBank, kind, truncate(id, 40), MaxIDLen)
	}
	return nil
}

// EntryIDs returns the entry ids in stable (sorted) order.
func (b Bank) EntryIDs() []string {
	ids := make([]string, 0, len(b.Entries))
	for id := range b.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Unattached counts bullets no entry lists. They have no block to live in, so
// they are reported, not synced.
func (b Bank) Unattached() int {
	used := map[string]bool{}
	for _, e := range b.Entries {
		for _, id := range e.Bullets {
			used[id] = true
		}
	}
	n := 0
	for id := range b.Bullets {
		if !used[id] {
			n++
		}
	}
	return n
}

// checkJSON requires exactly one top-level object, valid JSON, and no
// duplicate object keys (a decoder silently keeps the last one; rkb refuses,
// util.go:186).
func checkJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("top level is not an object")
	}
	if err := walkObject(dec, "$"); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}

// walkObject consumes members up to and including the closing brace.
func walkObject(dec *json.Decoder, path string) error {
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return fmt.Errorf("invalid JSON: %v", err)
		}
		key, _ := kt.(string)
		if seen[key] {
			return fmt.Errorf("duplicate key %q under %s", truncate(key, 40), path)
		}
		seen[key] = true
		if err := walkValue(dec, path+"."+key); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}

func walkValue(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		return walkObject(dec, path)
	case '[':
		for dec.More() {
			if err := walkValue(dec, path+"[]"); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("invalid JSON: %v", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}
