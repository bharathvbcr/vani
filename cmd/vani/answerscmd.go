package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/bharathvbcr/Vani/internal/semantic"
)

// maxSimilarInput caps the request read from stdin: MaxSimilarTexts wordings of
// MaxSimilarTextBytes each, as JSON escapes them, plus slack.
const maxSimilarInput = 8 << 20

// similarDefaultTimeout is the whole run, model load included. The Mac app waits
// 15 s (SimilarLabels.timeout) and kills the child after that, so this answers
// first, with a reason; finished batches stay cached for the next run.
const similarDefaultTimeout = 12 * time.Second

// answersInput is where `vani answers similar` reads its request. Tests replace it.
var answersInput io.Reader = os.Stdin

// cmdAnswers is `vani answers similar`.
func (e *env) cmdAnswers(args []string) error {
	if len(args) == 0 {
		return usagef("answers: expected subcommand \"similar\"")
	}
	if args[0] == "similar" {
		return e.cmdAnswersSimilar(args[1:])
	}
	return usagef("answers: unknown subcommand %q (want similar)", args[0])
}

type similarRequest struct {
	Texts  []string `json:"texts"`
	MinCos *float64 `json:"min_cos"`
}

type similarPairJSON struct {
	A    int     `json:"a"`
	B    int     `json:"b"`
	Cos  float64 `json:"cos"`
	Tier string  `json:"tier"`
}

// similarReply is the one JSON object `vani answers similar` writes. Pairs is
// always an array. It names indexes, never the wordings.
type similarReply struct {
	Available bool              `json:"available"`
	Reason    string            `json:"reason,omitempty"`
	Model     string            `json:"model,omitempty"`
	Pairs     []similarPairJSON `json:"pairs"`
	// Truncated counts wordings cut to semantic.MaxSimilarTextBytes.
	Truncated int `json:"truncated,omitempty"`
	// Note says what happened that the pairs alone do not: a cache started again.
	Note string `json:"note,omitempty"`
}

func unavailableSimilar(reason string, err error) similarReply {
	if err != nil {
		reason += ": " + oneLine(err.Error(), 200)
	}
	return similarReply{Reason: reason, Pairs: []similarPairJSON{}}
}

func (e *env) cmdAnswersSimilar(args []string) error {
	fs := e.newFlags("answers similar")
	fs.Bool("json", true, "accepted for the caller's sake: the reply is always one JSON object")
	timeout := fs.Duration("timeout", similarDefaultTimeout, fmt.Sprintf("whole run, model load included (1s to %v)", semantic.SimilarDeadline))
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `usage: vani answers similar [--json] [--timeout D]

Which question wordings read alike, by embedding. Reads one JSON object on stdin:
  {"texts":["LinkedIn profile URL", ...], "min_cos": 0.88}
and writes one on stdout:
  {"available":true, "model":"...", "pairs":[{"a":0,"b":3,"cos":0.97,"tier":"likely"}, ...]}
  {"available":false, "reason":"...", "pairs":[]}
a and b index texts (a < b). Send question keys and labels only, never answer
values: the texts are embedded and cached on disk (state dir semantic/).

tier is "likely" at cos >= %.2f unless the two wordings differ by one content word or a
contrast word (first/last, current/previous, now/future, ...; semantic.LexicalVeto), and
"possible" otherwise, down to min_cos. No false merge on the calibration fixture, which is
not a proof. A missing min_cos is %.2f. The pairs are
for a person to review; nothing is merged here.

Bounds: at most %d texts; a text over %d bytes is cut there at a rune boundary
(reply field "truncated"); at most %d pairs (raise min_cos past that); the run ends at
--timeout, with available:false and its reason. available:false is never "no pairs":
it covers a vani built without -tags tessl, no semantic.snapshot, a model that will not
load or answers badly, and a run out of time. Malformed input is {"error":...,"code":"runtime"} and exit 1.
`, semantic.LikelyCos, semantic.PossibleCos, semantic.MaxSimilarTexts, semantic.MaxSimilarTextBytes, semantic.MaxSimilarPairs)
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	if *timeout < time.Second || *timeout > semantic.SimilarDeadline {
		return usagef("answers similar: --timeout must be between 1s and %v", semantic.SimilarDeadline)
	}
	reply, err := e.similarOnce(*timeout)
	return e.reply(true, reply, err, nil)
}

// similarOnce runs one request. A non-nil error is a malformed request; whatever
// stops the check from running is in the reply as available:false.
func (e *env) similarOnce(timeout time.Duration) (similarReply, error) {
	raw, err := io.ReadAll(io.LimitReader(answersInput, maxSimilarInput+1))
	if err != nil {
		return similarReply{}, fmt.Errorf("answers similar: read request: %w", err)
	}
	if len(raw) > maxSimilarInput {
		return similarReply{}, fmt.Errorf("answers similar: request larger than %d bytes", maxSimilarInput)
	}
	var req similarRequest
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&req); err != nil {
		return similarReply{}, fmt.Errorf("answers similar: request is not {\"texts\":[...]}: %w", err)
	}
	minCos := semantic.PossibleCos
	if req.MinCos != nil {
		minCos = *req.MinCos
	}
	if math.IsNaN(minCos) || minCos < -1 || minCos > 1 {
		return similarReply{}, fmt.Errorf("answers similar: min_cos %v is outside -1..1", minCos)
	}
	if len(req.Texts) > semantic.MaxSimilarTexts {
		return unavailableSimilar(fmt.Sprintf("%d texts sent, at most %d are compared", len(req.Texts), semantic.MaxSimilarTexts), nil), nil
	}
	if len(req.Texts) < 2 {
		// Nothing to compare: the answer needs no model.
		return similarReply{Available: true, Pairs: []similarPairJSON{}}, nil
	}
	truncated := 0
	for i, t := range req.Texts {
		var cut bool
		if req.Texts[i], cut = semantic.ClipSimilarText(t); cut {
			truncated++
		}
	}

	cfg, err := e.loadConfig()
	if err != nil {
		return unavailableSimilar("the config could not be read", err), nil
	}
	if cfg.Semantic.Snapshot == "" {
		return unavailableSimilar("semantic.snapshot is not set in the config", nil), nil
	}
	ctx, stop := signalContext()
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	emb, closeEmb, err := openEmbedder(ctx, cfg)
	if err != nil {
		return unavailableSimilar("the embedding model could not be loaded", err), nil
	}
	defer func() { _ = closeEmb() }()

	var note string
	dir := semanticDir(e.stateDir)
	cache, err := semantic.OpenVectorCache(dir, 0)
	if errors.Is(err, semantic.ErrCache) {
		// A cache holds nothing the model cannot recompute: start it again, and say so.
		if rerr := semantic.ResetVectorCache(dir); rerr == nil {
			cache, err = semantic.OpenVectorCache(dir, 0)
			note = "the vector cache was unreadable and was started again"
		}
	}
	if err != nil {
		cache, note = nil, "the vector cache could not be opened, so nothing is cached: "+oneLine(err.Error(), 120)
	}

	pairs, rep, err := semantic.SimilarPairs(ctx, emb, cache, req.Texts, minCos)
	if err != nil {
		reason := "the similarity pass did not run"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = fmt.Sprintf("the similarity pass ran out of time (%v); %d of its wordings were embedded and are kept for the next run", timeout, rep.Scored)
			err = nil
		case errors.Is(err, semantic.ErrUnavailable):
			reason = "the embedding model is unavailable"
		case errors.Is(err, semantic.ErrBounds):
			reason = "the request is over a bound"
		case errors.Is(err, semantic.ErrEmbed):
			reason = "the embedding model answered badly"
		}
		r := unavailableSimilar(reason, err)
		r.Model, r.Truncated = emb.Model(), truncated
		return r, nil
	}
	if rep.Err != "" {
		note = oneLine(joinNote(note, rep.Err), 200)
	}
	out := make([]similarPairJSON, len(pairs))
	for i, p := range pairs {
		tier := semantic.TierPair(req.Texts[p.A], req.Texts[p.B], p.Cos)
		if tier == "" {
			tier = "possible" // below the standard floor because min_cos asked for it
		}
		out[i] = similarPairJSON{A: p.A, B: p.B, Cos: p.Cos, Tier: tier}
	}
	return similarReply{Available: true, Model: emb.Model(), Pairs: out, Truncated: truncated, Note: note}, nil
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
