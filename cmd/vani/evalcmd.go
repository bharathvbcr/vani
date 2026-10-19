package main

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"

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
	return eval.NewStore(labels, filepath.Join(e.stateDir, "eval", "text"))
}

// openLedger opens the ledger for commands that only read it. The
// ledger's lock still applies, so this refuses while the daemon is running.
func (e *env) openLedger() (*ledger.File, error) {
	l, err := ledger.Open(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return l, nil
}

func (e *env) cmdLabel(args []string) error {
	fs := e.newFlags("label")
	n := fs.Int("n", 20, "how many postings to label this session")
	labels := fs.String("labels", "", "label directory (default: state dir; use the repo's eval/jd_triage to keep labels in git)")
	seed := fs.Int64("seed", 0, "shuffle seed (0: time-based)")
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
	l, err := e.openLedger()
	if err != nil {
		return err
	}
	defer l.Close()
	if _, bad, err := s.Labels(); err != nil {
		return fmt.Errorf("labels unreadable, refusing to queue (would repeat work): %w", err)
	} else if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	sd := *seed
	if sd == 0 {
		sd = now().UnixNano()
	}
	queue := eval.Queue(l.All(), s, *n, rand.New(rand.NewSource(sd)))
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
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (e *env) cmdEval(args []string) error {
	fs := e.newFlags("eval")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	l, err := e.openLedger()
	if err != nil {
		return err
	}
	defer l.Close()
	names, err := e.evalNames()
	if err != nil {
		return err
	}
	fmt.Fprint(e.stdout, eval.BuildReport(l.All(), s, names).Markdown())
	return nil
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
