package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// maxImpressionsStdin bounds the request of `vani impressions log`, and
// maxImpressionRows the rows in it: a screen shows a screenful, not a ledger.
const (
	maxImpressionsStdin = 256 << 10
	maxImpressionRows   = 200
)

// recordImpressions logs every role a written digest lists, with its section,
// position, score and, for an Explore row, its stratum and inclusion
// probability, in one slate (the digest's date). Nothing shown logs nothing.
func recordImpressions(ctx context.Context, stateDir string, at time.Time, slate string, shown []digest.Shown) error {
	rows := make([]feedback.Impression, 0, len(shown))
	for _, s := range shown {
		rows = append(rows, feedback.Impression{At: at, SlateID: slate, Surface: feedback.SurfaceDigest, Section: s.Section,
			Key: s.Key, Position: s.Position, Status: string(s.Status), DropReason: s.DropClass,
			Source: s.Source, PSelect: s.PSelect, Scores: feedback.ScoresOf(s.Result)})
	}
	return appendImpressionRows(ctx, stateDir, rows)
}

// recordAuditCards logs the audit cards a session showed, in draw order, as one
// slate named by the draw's seed.
func recordAuditCards(ctx context.Context, stateDir string, at time.Time, seed string, picks []eval.AuditPick) error {
	rows := make([]feedback.Impression, 0, len(picks))
	for i, p := range picks {
		rows = append(rows, feedback.Impression{At: at, SlateID: slateToken(seed), Surface: feedback.SurfaceAudit, Section: slateToken(p.Source),
			Key: p.Key, Position: i + 1, Source: slateToken(p.Source), PSelect: p.PSelect})
	}
	return appendImpressionRows(ctx, stateDir, rows)
}

// slateToken is s as a feedback token (lower case; anything outside letters,
// digits and _:-. becomes -), at most 64 bytes; an ISO-week or date seed passes
// through unchanged. An empty s stays empty.
func slateToken(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 64; i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == ':', c == '-', c == '.':
		default:
			c = '-'
		}
		b = append(b, c)
	}
	return string(b)
}

// appendImpressionRows writes rows in appends the log takes, skipping what the
// slate already holds.
func appendImpressionRows(ctx context.Context, stateDir string, rows []feedback.Impression) error {
	for len(rows) > 0 {
		n := min(len(rows), feedback.MaxAppend)
		if _, err := feedback.AppendImpressions(ctx, stateDir, rows[:n]); err != nil {
			return err
		}
		rows = rows[n:]
	}
	return nil
}

// impressionsRequest is `vani impressions log`'s stdin: the rows a screen
// shows, by key and 1-based position. Nothing else about a posting is sent or
// kept.
type impressionsRequest struct {
	Rows []struct {
		Key      posting.Key `json:"key"`
		Position int         `json:"position"`
	} `json:"rows"`
}

// cmdImpressions is `vani impressions log`: a screen (the Jobs tab, the Audit
// tab) reports the top rows it is showing, so an unmarked posting can count as
// seen-and-passed. The request is JSON on stdin; rows already logged for the
// same slate are skipped, so a refresh reports the same slate again for free.
func (e *env) cmdImpressions(args []string) error {
	if len(args) == 0 || args[0] != "log" {
		return usagef("impressions: want the subcommand log")
	}
	fs := e.newFlags("impressions log")
	surface := fs.String("surface", "", "jobs or audit (the digest records its own)")
	slate := fs.String("slate", "", "the slate being shown (a view and refresh id, a draw seed); a key is logged once per slate")
	section := fs.String("section", "", "the view or section shown (optional)")
	asJSON := e.jsonFlag(fs, `print {"logged":N} (or {"error"}) on stdout`)
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("impressions log: unexpected argument %q", fs.Arg(0))
	}
	if *surface != feedback.SurfaceJobs && *surface != feedback.SurfaceAudit {
		return usagef("impressions log: --surface must be %q or %q", feedback.SurfaceJobs, feedback.SurfaceAudit)
	}
	if *slate == "" {
		return usagef("impressions log: --slate is required (it is what makes a refresh count once)")
	}
	n, err := e.logImpressions(*surface, *slate, *section)
	return e.reply(*asJSON, struct {
		Logged int `json:"logged"`
	}{n}, err, func() { fmt.Fprintf(e.stdout, "logged %d impression(s)\n", n) })
}

func (e *env) logImpressions(surface, slate, section string) (int, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxImpressionsStdin+1))
	if err != nil {
		return 0, fmt.Errorf("impressions log: read stdin: %w", err)
	}
	if len(data) > maxImpressionsStdin {
		return 0, fmt.Errorf("impressions log: request over %d bytes", maxImpressionsStdin)
	}
	var req impressionsRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return 0, fmt.Errorf("impressions log: request must be {\"rows\":[{\"key\",\"position\"}]}: %w", err)
	}
	if len(req.Rows) > maxImpressionRows {
		return 0, fmt.Errorf("impressions log: %d rows, at most %d", len(req.Rows), maxImpressionRows)
	}
	at := now()
	rows := make([]feedback.Impression, 0, len(req.Rows))
	for _, r := range req.Rows {
		rows = append(rows, feedback.Impression{At: at, SlateID: slate, Surface: surface, Section: section, Key: r.Key, Position: r.Position})
	}
	return feedback.AppendImpressions(context.Background(), e.stateDir, rows)
}
