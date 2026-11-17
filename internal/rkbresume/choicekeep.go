package rkbresume

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
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

// keptChoiceSkew is how far ahead of now an entry's mtime may be before it is
// treated as dated by a wrong clock: such an entry is forgotten, never reused.
const keptChoiceSkew = 5 * time.Minute

// load returns the reply kept under key, when there is one that is whole, small
// and has not expired at now. Only a regular file at the key path is read: a
// symlink or a FIFO there is a miss and is never followed or opened blocking.
func (k choiceKeep) load(key string, now time.Time) (string, bool) {
	p, ok := k.path(key)
	if !ok {
		return "", false
	}
	lst, err := os.Lstat(p)
	if err != nil {
		return "", false
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(p) // the link itself, never its target
		return "", false
	}
	if !lst.Mode().IsRegular() {
		return "", false
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(lst, st) {
		return "", false
	}
	if st.Size() > maxKeptChoiceBytes+1024 || now.Sub(st.ModTime()) > keptChoiceTTL || st.ModTime().Sub(now) > keptChoiceSkew {
		_ = os.Remove(p)
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

// private makes the folder, and tightens one that already exists wider than
// 0700. A folder that cannot be tightened (another owner's) keeps nothing.
func (k choiceKeep) private() bool {
	if os.MkdirAll(k.dir, 0o700) != nil {
		return false
	}
	st, err := os.Stat(k.dir)
	if err != nil || !st.IsDir() {
		return false
	}
	if st.Mode().Perm()&0o077 != 0 {
		return os.Chmod(k.dir, 0o700) == nil
	}
	return true
}

// save keeps reply under key, then expires old entries and drops the oldest past
// the bound. It is best effort: a reply that cannot be kept only costs a retry.
func (k choiceKeep) save(key, reply string, now time.Time) {
	p, ok := k.path(key)
	if !ok || reply == "" || len(reply) > maxKeptChoiceBytes {
		return
	}
	raw, err := json.Marshal(keptChoice{V: keptChoiceVersion, Reply: reply})
	if err != nil || !k.private() || atomicfile.Write(p, raw) != nil {
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
		if now.Sub(info.ModTime()) > keptChoiceTTL || info.ModTime().Sub(now) > keptChoiceSkew {
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
