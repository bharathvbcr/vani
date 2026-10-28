package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// vani's JSON contract, the one an agent (or the Mac app) relies on. Under the
// global flag (`vani --json <command> ...`, CLI-Anything's placement) every
// outcome is exactly one JSON value on stdout, a failure of any kind being
// {"error","code"}. Under a command's own --json, or for a command whose
// output is JSON regardless, a runtime failure is that envelope too, and a
// usage error keeps its old form: exit 2, nothing on stdout. Exit codes are
// unchanged: 0 ok, 1 runtime, 2 usage. skills/cli-anything-vani/SKILL.md is
// the agent-facing description of it.

// jsonMode is how one leaf command answers in JSON.
type jsonMode string

const (
	jsonFlag   jsonMode = "flag"   // takes --json
	jsonAlways jsonMode = "always" // stdout is JSON whatever the flags
	jsonNone   jsonMode = "none"   // no JSON form by design; reason says why
)

// Codes the envelope carries when the command wrote no reply of its own. A
// command's own codes (draft, rkbresume) are snake_case nouns too.
const (
	codeUsage       = "usage"
	codeRuntime     = "runtime"
	codeUnsupported = "unsupported" // the global --json on a command with no JSON form
	codeBusy        = "busy"        // another vani holds the ledger; retry when it is done
)

// jsonLeaf is one leaf command: the words that name it, as usageTemplate
// spells them.
type jsonLeaf struct {
	Command string   `json:"command"`
	Mode    jsonMode `json:"json"`
	Reason  string   `json:"reason,omitempty"`
}

// jsonLeaves is every leaf command vani dispatches. TestJSONLeavesMatchUsage
// holds it to usageTemplate and TestJSONLeavesMatchFlags to the flags each
// command declares, so it cannot drift from either.
var jsonLeaves = []jsonLeaf{
	{"version", jsonFlag, ""},
	{"init", jsonFlag, ""},
	{"config check", jsonFlag, ""},
	{"config motivation", jsonAlways, ""},
	{"config exclude", jsonAlways, ""},
	{"watchlist list", jsonFlag, ""},
	{"watchlist add", jsonFlag, ""},
	{"watchlist remove", jsonFlag, ""},
	{"watchlist domain", jsonFlag, ""},
	{"watchlist icon", jsonFlag, ""},
	{"watchlist discover", jsonFlag, ""},
	{"logos refresh", jsonFlag, ""},
	{"daemon", jsonNone, "runs the scheduler in the foreground until stopped; read it with status --json and runs show --json"},
	{"run-once", jsonFlag, ""},
	{"run-request", jsonFlag, ""},
	{"digest", jsonFlag, ""},
	{"status", jsonFlag, ""},
	{"runs list", jsonFlag, ""},
	{"runs show", jsonFlag, ""},
	{"runs failures", jsonFlag, ""},
	{"label", jsonNone, "asks the owner one posting at a time on the terminal"},
	{"export heldout", jsonFlag, ""},
	{"eval", jsonFlag, ""},
	{"audit", jsonFlag, ""},
	{"audit answer", jsonFlag, ""},
	{"relevance", jsonFlag, ""},
	{"rankfit", jsonFlag, ""},
	{"lca import", jsonFlag, ""},
	{"lca lookup", jsonFlag, ""},
	{"employer import", jsonFlag, ""},
	{"employer lookup", jsonFlag, ""},
	{"track list", jsonFlag, ""},
	{"track show", jsonFlag, ""},
	{"track use", jsonFlag, ""},
	{"kb sync", jsonFlag, ""},
	{"reasons draft", jsonFlag, ""},
	{"reasons import", jsonFlag, ""},
	{"reasons check", jsonFlag, ""},
	{"kbmatch run", jsonFlag, ""},
	{"kbmatch status", jsonFlag, ""},
	{"teacher", jsonFlag, ""},
	{"impressions log", jsonFlag, ""},
	{"labels", jsonFlag, ""},
	{"jobs list", jsonFlag, ""},
	{"jobs stats", jsonFlag, ""},
	{"jobs export", jsonFlag, ""},
	{"jobs folders", jsonFlag, ""},
	{"jobs show", jsonFlag, ""},
	{"jobs find", jsonFlag, ""},
	{"jobs ingest", jsonFlag, ""},
	{"jobs mark", jsonFlag, ""},
	{"jobs note", jsonFlag, ""},
	{"jobs retriage", jsonFlag, ""},
	{"jobs resume build", jsonFlag, ""},
	{"jobs resume render", jsonFlag, ""},
	{"jobs resume show", jsonFlag, ""},
	{"jobs resume use", jsonFlag, ""},
	{"jobs resume revalidate", jsonFlag, ""},
	{"jobs resume diff", jsonFlag, ""},
	{"jobs resume current", jsonFlag, ""},
	{"jobs resume coverage", jsonFlag, ""},
	{"jobs resume gaps", jsonFlag, ""},
	{"jobs gaps", jsonFlag, ""},
	{"retriage", jsonFlag, ""},
	{"rescore", jsonFlag, ""},
	{"prefilter", jsonFlag, ""},
	{"bench local", jsonFlag, ""},
	{"semantic warm", jsonFlag, ""},
	{"semantic status", jsonFlag, ""},
	{"answers similar", jsonAlways, ""},
	{"draft", jsonAlways, ""},
	{"suggest", jsonAlways, ""},
	{"resume", jsonAlways, ""},
	{"migrate rekey-fallback", jsonFlag, ""},
	{"launchd plist", jsonNone, "its output is a launchd plist (XML) to install"},
}

// commandAliases are the old or second names a dispatcher accepts for one
// word of a command, keyed by the command words before it ("" at the top).
var commandAliases = map[string]map[string]string{
	"":     {"audit-sample": "audit", "--version": "version"},
	"jobs": {"categories": "folders"},
}

// leafOf finds the leaf command args start with: the longest jsonLeaves entry
// whose words lead args, aliases read as their canonical names. n is how many
// of args it took; ok is false when args name no leaf (an unknown command, or
// a command group with no subcommand), which the dispatcher reports.
func leafOf(args []string) (leaf jsonLeaf, n int, ok bool) {
	var words []string
	for i := 0; i < len(args) && i < 3; i++ {
		w := args[i]
		if canon, alias := commandAliases[strings.Join(words, " ")][w]; alias {
			w = canon
		}
		words = append(words, w)
		name := strings.Join(words, " ")
		for _, l := range jsonLeaves {
			if l.Command == name {
				leaf, n, ok = l, i+1, true
			}
		}
	}
	return leaf, n, ok
}

// splitGlobalJSON strips the leading --json flags `vani --json <command>`
// carries and reports whether JSON was asked for. As with a command's own
// flag, --json=V takes a boolean V (strconv.ParseBool) and the last one
// decides; any other V is a usage error.
func splitGlobalJSON(args []string) (rest []string, global bool, err error) {
	for len(args) > 0 {
		name, val, hasVal := strings.Cut(args[0], "=")
		if name != "--json" && name != "-json" {
			break
		}
		on := true
		if hasVal {
			if on, err = strconv.ParseBool(val); err != nil {
				return nil, true, usagef("invalid value %q for --json: want true or false", val)
			}
		}
		args, global = args[1:], on
	}
	return args, global, nil
}

// jsonFlag declares a command's --json flag and records it on e, so runEnv
// knows the command was asked for JSON when it fails without replying.
func (e *env) jsonFlag(fs *flag.FlagSet, usage string) *bool {
	p := fs.Bool("json", false, usage)
	e.jsonAsked = p
	return p
}

// errorReply is the envelope every JSON failure has: the message and a code.
type errorReply struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// failure is the envelope inside a reply that keeps its data when the command
// fails: a list with unreadable entries, a check that found problems. A reply
// struct embeds it and is passed to reply as a pointer.
type failure struct {
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

func (f *failure) fail(err error) { f.Error, f.Code = err.Error(), envelopeCode(err) }

// textToStderr runs fn with the text a command prints as it goes sent to
// stderr when asJSON, so stdout holds the JSON reply alone: a run's or a
// digest's progress stays readable without breaking the reply.
func (e *env) textToStderr(asJSON bool, fn func() error) error {
	if !asJSON {
		return fn()
	}
	out := e.stdout
	e.stdout = e.stderr
	defer func() { e.stdout = out }()
	return fn()
}

// reply writes v as JSON and returns the exit error. When err is set it writes
// v with its failure filled in if v embeds one, else {"error","code"} alone. A
// usage error stays a usage error (runEnv decides whether it is JSON). Without
// asJSON it calls text when err is nil and returns err.
func (e *env) reply(asJSON bool, v any, err error, text func()) error {
	if !asJSON {
		if err == nil {
			text()
		}
		return err
	}
	if err != nil {
		if errors.As(err, new(usageError)) || errors.Is(err, flag.ErrHelp) {
			return err
		}
		if f, ok := v.(interface{ fail(error) }); ok {
			f.fail(err)
		} else {
			v = errorReply{err.Error(), envelopeCode(err)}
		}
	}
	if werr := writeJSON(e.stdout, v); werr != nil {
		return werr
	}
	if err != nil {
		return errAlreadyReported
	}
	return nil
}

// writeJSON writes v as one line of JSON, HTML characters unescaped.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// jsonInventory is `vani --json help`: every leaf command and how it answers
// in JSON, so an agent learns the surface without parsing the usage text.
type jsonInventory struct {
	Commands []jsonLeaf `json:"commands"`
}

// errNoJSON is the global --json on a command that has no JSON form.
func errNoJSON(l jsonLeaf) error {
	return unsupportedError{fmt.Sprintf("vani %s: it has no JSON output: %s", l.Command, l.Reason)}
}

// unsupportedError exits 2 like a usage error, with its own code.
type unsupportedError struct{ msg string }

func (e unsupportedError) Error() string { return e.msg }

// countingWriter counts what passes through to w, so runEnv can tell whether a
// command wrote a reply before it failed. The daemon writes from more than one
// goroutine, hence the atomic.
type countingWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// envelopeCode is the code a failure's envelope carries.
func envelopeCode(err error) string {
	switch {
	case errors.As(err, new(unsupportedError)):
		return codeUnsupported
	case errors.As(err, new(usageError)):
		return codeUsage
	case errors.Is(err, ledger.ErrLocked):
		return codeBusy
	default:
		return codeRuntime
	}
}
