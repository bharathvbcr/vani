package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// stdin is the label session's input; a variable so tests can script it.
var stdin io.Reader = os.Stdin

// maxLabelBatch bounds one label session.
const maxLabelBatch = 200

// openEvalStore opens the label store. Labels default to the state dir; point
// --labels at the repo's eval/jd_triage to keep them in git. JD text always
// stays in the state dir, so git only ever holds hashes.
func (e *env) openEvalStore(labels string) (*eval.Store, error) {
	if labels == "" {
		labels = filepath.Join(e.stateDir, "eval", "labels")
	}
	s, err := eval.NewStore(labels, filepath.Join(e.stateDir, "eval", "text"))
	if err != nil {
		return nil, err
	}
	s.AfterWrite = func() { e.reexportHeldout(s) }
	return s, nil
}

// ledgerRecords reads the ledger for commands that only read it. It takes no
// lock (ledger.ReadSnapshot), so it works while the daemon or a run holds the
// ledger; a line a writer is still appending is left out, with a note. A
// ledger that was never written is empty.
func (e *env) ledgerRecords() ([]ledger.Record, error) {
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if snap.Torn {
		fmt.Fprintln(e.stderr, "vani: the ledger's last line is still being written; it is left out")
	}
	return snap.Records, nil
}

func (e *env) cmdLabel(args []string) error {
	fs := e.newFlags("label")
	n := fs.Int("n", 20, "how many postings to label this session")
	labels := fs.String("labels", "", "label directory (default: state dir; use the repo's eval/jd_triage to keep labels in git)")
	seed := fs.Int64("seed", 0, "shuffle seed (0: time-based)")
	window := fs.String("window", "", "label only postings the shadow was asked about, for this window ("+strings.Join(eval.WindowNames(), ", ")+"); it must be the window now filling")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *n < 1 || *n > maxLabelBatch {
		return usagef("label: -n must be within 1..%d", maxLabelBatch)
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	recs, err := e.ledgerRecords()
	if err != nil {
		return err
	}
	var qopts eval.QueueOptions
	if *window != "" {
		w, err := eval.LookupWindow(*window)
		if err != nil {
			return usagef("label: %v", err)
		}
		if _, err := s.SyncWindows(recs); err != nil {
			return err
		}
		open, ok, err := s.OpenWindow()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("label: every window is closed")
		}
		if open.Name != w.Name {
			return usagef("label: %s is not filling; labels go to %s first", w.Name, open.Name)
		}
		free, err := s.WindowFree(w)
		if err != nil {
			return err
		}
		*n = min(*n, free)
		qopts.ShadowOnly = true
	}
	if t := e.teacherReadings(); t != nil {
		qopts.Teacher = t
	}
	if _, bad, err := s.Labels(); err != nil {
		return fmt.Errorf("labels unreadable, refusing to queue (would repeat work): %w", err)
	} else if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	sd := *seed
	if sd == 0 {
		sd = now().UnixNano()
	}
	queue, err := eval.QueueWith(recs, s, *n, rand.New(rand.NewSource(sd)), e.dropRules(), qopts)
	if err != nil {
		return fmt.Errorf("labels unreadable, refusing to queue (would repeat work): %w", err)
	}
	if len(queue) == 0 {
		fmt.Fprintln(e.stdout, "nothing to label: no unlabelled triaged postings")
		return nil
	}
	names, err := e.evalNames()
	if err != nil {
		return err
	}
	done, err := eval.Label(stdin, e.stdout, queue, s, names)
	fmt.Fprintf(e.stdout, "labelled %d of %d queued\n", done, len(queue))
	if *window != "" {
		if _, serr := s.SyncWindows(recs); serr != nil && err == nil {
			err = serr
		}
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// teacherReadings opens the second teacher's readings log for the label queue,
// as the default teacher model (llm.triage_model, else llm.model) and the
// configured target summary ask it now; a reading made under another model or
// prompt is not current and does not order the queue. It reads without the run
// lock (rows are appended whole and a torn tail is skipped). Everything that
// is not a usable store (never written, empty, unreadable, a config that does
// not name a model or summary) yields nil, the queue's order without a
// teacher: labelling is never blocked by it. Only an unreadable store or a
// bad summary is worth a warning; a missing store is the normal case.
func (e *env) teacherReadings() eval.TeacherReadings {
	path := filepath.Join(e.stateDir, eval.TeacherFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return nil // evalNames reports an unloadable config
	}
	model := cfg.LLM.EffectiveTriageModel()
	if model == "" {
		return nil
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: teacher readings not used for the queue: %v\n", err)
		return nil
	}
	store, err := eval.OpenTeacherStore(path)
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: teacher readings unreadable, queueing without them: %v\n", err)
		return nil
	}
	if store.Len() == 0 {
		return nil
	}
	return store.Current(model, summary)
}

func (e *env) cmdEval(args []string) error {
	fs := e.newFlags("eval")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	heldOut := fs.String("held-out", "", "also report Promotion over a registered held-out split ("+strings.Join(eval.SplitNames(), ", ")+")")
	windows := fs.Bool("window", false, "also report each label window's Promotion table and the per-slot verdict (promotable only if every window is closed and promotable)")
	asJSON := e.jsonFlag(fs, `{"report","held_out"?,"windows"?} on stdout: the raw counts the tables are built from`)
	if err := parse(fs, args); err != nil {
		return err
	}
	var split eval.Split
	if *heldOut != "" {
		sp, err := eval.LookupSplit(*heldOut)
		if err != nil {
			return usagef("eval: %v", err)
		}
		split = sp
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	recs, err := e.ledgerRecords()
	if err != nil {
		return err
	}
	names, err := e.evalNames()
	if err != nil {
		return err
	}
	// Every table is built before anything is printed, so a label file one of
	// them refuses yields an error and no figures at all.
	var out evalReply
	if *windows {
		// BuildWindows files in memory only: `vani label --window` is the one
		// writer of windows.json.
		wr, err := eval.BuildWindows(recs, s, names)
		if err != nil {
			return err
		}
		out.Windows = &wr
	}
	if *heldOut == "" {
		out.Report = eval.BuildReport(recs, s, names)
	} else {
		all, h, err := eval.BuildHeldOut(recs, s, names, split)
		if err != nil {
			return err
		}
		out.Report, out.HeldOut = all, &h
	}
	return e.reply(*asJSON, out, nil, func() {
		md := out.Report.Markdown()
		if out.HeldOut != nil {
			md += out.HeldOut.Markdown()
		}
		if out.Windows != nil {
			md += out.Windows.Markdown()
		}
		fmt.Fprint(e.stdout, md)
	})
}

// evalReply is `vani eval --json`: the counts behind each table, with the
// held-out split and the windows when asked for.
type evalReply struct {
	Report  eval.Report        `json:"report"`
	HeldOut *eval.HeldOut      `json:"held_out,omitempty"`
	Windows *eval.WindowReport `json:"windows,omitempty"`
}

// evalNames reads the configured OpenRouter backend's name, which the eval
// files old rows' legacy primary name under. Without the config the numbers
// would split one backend in two, so a config that cannot load is an error.
func (e *env) evalNames() (eval.Names, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return eval.Names{}, fmt.Errorf("eval needs the config to name the OpenRouter backend: %w", err)
	}
	var n eval.Names
	if cfg.OpenRouterConfigured() {
		n.OpenRouter = cfg.Decide.OpenRouterName()
	}
	return n, nil
}
