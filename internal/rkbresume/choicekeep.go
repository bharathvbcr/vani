package rkbresume

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// A model reply that validated is paid for. When the build then fails after it
// (rkb fit could not run, the deadline hit, the caller stopped), the reply is kept
// here so a retry of the same inputs builds it again instead of buying another.
//
// A kept reply is found by a key over everything that determines the turn: the
// model, the system prompt, every message sent (which carry the posting, the
// eligible pool, the base skeleton and the owner's instruction) and the
// fingerprints of what the build read. Any change to those is a different key, so
// a stale choice is never reused for changed inputs. Entries expire, and the
// folder is bounded in count and in size.
const (
	keptChoiceTTL      = 24 * time.Hour
	maxKeptChoices     = 32
	maxKeptChoiceBytes = 64 << 10
	keptChoiceVersion  = 1
)

var keptKeyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// choiceKeep is the kept replies of one folder; the zero value (no dir) keeps nothing.
type choiceKeep struct{ dir string }

type keptChoice struct {
	V     int    `json:"v"`
	Reply string `json:"reply"`
}

// keptChoiceKey names the turn msgs would send for this build.
func (b *builder) keptChoiceKey(msgs []llm.Message) string {
	h := []string{b.cfg.Model, systemPrompt, b.instr, b.cfg.Tailor.Scope, strings.Join(b.cfg.Tailor.Lock, "\x00")}
	if fp, err := json.Marshal(b.fp); err == nil {
		h = append(h, string(fp))
	}
	for _, m := range msgs {
		h = append(h, string(m.Role)+"\x01"+m.Text())
	}
	return sha256Hex([]byte(strings.Join(h, "\x02")))
}

func (k choiceKeep) path(key string) (string, bool) {
	if k.dir == "" || !keptKeyRE.MatchString(key) {
		return "", false
	}
	return filepath.Join(k.dir, key+".json"), true
}

// load returns the reply kept under key, when there is one that is whole, small
// and has not expired at now.
func (k choiceKeep) load(key string, now time.Time) (string, bool) {
	p, ok := k.path(key)
	if !ok {
		return "", false
	}
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxKeptChoiceBytes+1024 || now.Sub(st.ModTime()) > keptChoiceTTL {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxKeptChoiceBytes+1025))
	if err != nil {
		return "", false
	}
	var c keptChoice
	if json.Unmarshal(raw, &c) != nil || c.V != keptChoiceVersion || c.Reply == "" || len(c.Reply) > maxKeptChoiceBytes {
		return "", false
	}
	return c.Reply, true
}

// save keeps reply under key, then expires old entries and drops the oldest past
// the bound. It is best effort: a reply that cannot be kept only costs a retry.
func (k choiceKeep) save(key, reply string, now time.Time) {
	p, ok := k.path(key)
	if !ok || reply == "" || len(reply) > maxKeptChoiceBytes {
		return
	}
	raw, err := json.Marshal(keptChoice{V: keptChoiceVersion, Reply: reply})
	if err != nil || os.MkdirAll(k.dir, 0o700) != nil || atomicfile.Write(p, raw) != nil {
		return
	}
	_ = os.Chtimes(p, now, now)
	k.sweep(now)
}

// remove forgets key's reply.
func (k choiceKeep) remove(key string) {
	if p, ok := k.path(key); ok {
		_ = os.Remove(p)
	}
}

func (k choiceKeep) sweep(now time.Time) {
	ents, err := os.ReadDir(k.dir)
	if err != nil {
		return
	}
	type kept struct {
		path string
		at   time.Time
	}
	var live []kept
	for _, e := range ents {
		key, isJSON := strings.CutSuffix(e.Name(), ".json")
		if !isJSON || !keptKeyRE.MatchString(key) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		p := filepath.Join(k.dir, e.Name())
		if now.Sub(info.ModTime()) > keptChoiceTTL {
			_ = os.Remove(p)
			continue
		}
		live = append(live, kept{p, info.ModTime()})
	}
	if len(live) <= maxKeptChoices {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].at.Before(live[j].at) })
	for _, e := range live[:len(live)-maxKeptChoices] {
		_ = os.Remove(e.path)
	}
}
