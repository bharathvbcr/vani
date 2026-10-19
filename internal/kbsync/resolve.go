package kbsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Resolution decision. rkb owns claim values: Claims.tex is the only place a
// figure's value lives and rkb detexes it (rkb/claims.go:50-127,
// numbers.go:53). The one machine-readable export is
// `rkb claims export --out <file>` (claims.go:497-530), which writes
// claims.<id>.value for every claim and touches neither bank.json nor the
// verify log. No rkb command emits resolved bullet text (`compose` keeps the
// tokens, bank.go:796-805), so Vani asks rkb for the values and does only the
// mechanical {{clmX}} substitution itself, with the same token pattern and
// the same plain-text markup rules as rkb's own text renderer.

// Bounds for the rkb subprocess.
const (
	DefaultRKBTimeout = 60 * time.Second
	MaxRKBTimeout     = 5 * time.Minute
	MaxClaimsBytes    = 4 << 20
	maxProcOutput     = 64 << 10
)

// ErrResolve wraps every claim-resolution failure.
var ErrResolve = errors.New("kbsync: resolve claims")

// tokenRe is rkb/render.go:12.
var tokenRe = regexp.MustCompile(`\{\{(clm[A-Za-z]+)\}\}`)

// Plain-text markup, mirroring rkb/render.go:13-15 and the text renderer
// (render.go:99-114), so resolved text matches rkb's own .txt output and no
// markdown reaches DevPrism's LaTeX path.
var (
	boldRe   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicRe = regexp.MustCompile(`(^|[^*\w])\*([^*]+?)\*($|[^*\w])`)
	linkRe   = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)
)

// Command is one subprocess invocation. Env is the complete environment.
type Command struct {
	Path string
	Args []string
	Env  []string
}

// Runner runs a subprocess. It is the seam tests fake: the fake plays rkb and
// writes the --out file it is handed.
type Runner interface {
	Run(ctx context.Context, c Command) error
}

// ExecRunner runs real processes with a bounded timeout and bounded captured
// output. Its Timeout is clamped to MaxRKBTimeout.
type ExecRunner struct {
	Timeout time.Duration
}

type capWriter struct {
	buf bytes.Buffer
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := maxProcOutput - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil // keep draining so a noisy child cannot block
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, c Command) error {
	to := r.Timeout
	if to <= 0 {
		to = DefaultRKBTimeout
	}
	if to > MaxRKBTimeout {
		to = MaxRKBTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = c.Env
	cmd.Stdin = nil
	var out, errw capWriter
	cmd.Stdout, cmd.Stderr = &out, &errw
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: rkb: %v", ErrResolve, ctx.Err())
		}
		return fmt.Errorf("%w: rkb: %v: %s", ErrResolve, err, truncate(strings.TrimSpace(errw.buf.String()), 400))
	}
	return nil
}

// RKB resolves claim values by calling the rkb binary.
type RKB struct {
	Binary   string // absolute path to the rkb binary
	CodeRoot string // the folder holding career/ (rkb's RKB_CODE_ROOT); empty lets rkb search
	Runner   Runner // nil means ExecRunner with the default timeout
}

// Claims runs `rkb claims export` into a private temp dir and returns
// claim id -> value, plus the ids rkb flags with a CAUTION note. A failing
// export (broken Claims.tex, missing binary) is an error: no claims are
// guessed.
func (k RKB) Claims(ctx context.Context) (Claims, error) {
	if !filepath.IsAbs(k.Binary) {
		return Claims{}, fmt.Errorf("%w: rkb binary path %q is not absolute", ErrResolve, k.Binary)
	}
	run := k.Runner
	if run == nil {
		run = ExecRunner{}
	}
	dir, err := os.MkdirTemp("", "vani-kbsync-")
	if err != nil {
		return Claims{}, fmt.Errorf("%w: temp dir: %v", ErrResolve, err)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "claims.json")
	env := minimalEnv()
	if k.CodeRoot != "" {
		env = append(env, "RKB_CODE_ROOT="+k.CodeRoot)
	}
	if err := run.Run(ctx, Command{Path: k.Binary, Args: []string{"claims", "export", "--out", out}, Env: env}); err != nil {
		return Claims{}, err
	}
	f, err := os.Open(out)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: rkb wrote no claims file: %v", ErrResolve, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxClaimsBytes+1))
	if err != nil {
		return Claims{}, fmt.Errorf("%w: read claims: %v", ErrResolve, err)
	}
	if len(raw) > MaxClaimsBytes {
		return Claims{}, fmt.Errorf("%w: claims file larger than %d bytes", ErrResolve, MaxClaimsBytes)
	}
	return ParseClaims(raw)
}

func minimalEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Claims holds resolved claim values and the ids rkb marked CAUTION.
type Claims struct {
	Values  map[string]string
	Caution map[string]bool
}

// ParseClaims reads rkb's claims.json (rkb/render.go:26-53). An empty claim
// value is refused: it would erase a figure from a bullet.
func ParseClaims(raw []byte) (Claims, error) {
	var cf struct {
		Claims map[string]struct {
			Value   string `json:"value"`
			Caution string `json:"caution"`
		} `json:"claims"`
	}
	if err := json.Unmarshal(raw, &cf); err != nil {
		return Claims{}, fmt.Errorf("%w: claims.json: %v", ErrResolve, err)
	}
	if len(cf.Claims) == 0 {
		return Claims{}, fmt.Errorf("%w: claims.json holds no claims", ErrResolve)
	}
	c := Claims{Values: map[string]string{}, Caution: map[string]bool{}}
	for id, v := range cf.Claims {
		if strings.TrimSpace(v.Value) == "" {
			return Claims{}, fmt.Errorf("%w: claim %s has an empty value", ErrResolve, id)
		}
		c.Values[id] = v.Value
		if v.Caution != "" {
			c.Caution[id] = true
		}
	}
	return c, nil
}

// ResolveText substitutes claim tokens and strips inline markup the way rkb's
// text renderer does. It returns the token ids it could not resolve, in
// first-seen order; the text still holds those tokens untouched.
func ResolveText(s string, claims map[string]string) (string, []string) {
	var missing []string
	seen := map[string]bool{}
	s = tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		id := tokenRe.FindStringSubmatch(m)[1]
		if v, ok := claims[id]; ok {
			return v
		}
		if !seen[id] {
			seen[id] = true
			missing = append(missing, id)
		}
		return m
	})
	s = boldRe.ReplaceAllString(s, "$1")
	s = italicRe.ReplaceAllString(s, "$1$2$3")
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		g := linkRe.FindStringSubmatch(m)
		if strings.Contains(g[2], strings.TrimPrefix(strings.TrimPrefix(g[1], "https://"), "http://")) {
			return g[1]
		}
		return g[1] + " (" + g[2] + ")"
	})
	return s, missing
}

// tokenIDs lists the distinct claim ids a text uses, sorted.
func tokenIDs(s string) []string {
	set := map[string]bool{}
	for _, m := range tokenRe.FindAllStringSubmatch(s, -1) {
		set[m[1]] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
