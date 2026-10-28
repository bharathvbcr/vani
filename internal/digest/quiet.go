package digest

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// QuietDays is how many whole days an application waits without the owner
	// recording a change (the applied mark, or a stage) before the digest lists
	// it under "Quiet since applied". A fixed number, not a setting: it is a
	// nudge to follow up, and ten days is about when a silent application
	// starts to be worth a message.
	QuietDays = 10
	// MaxQuiet bounds the rows listed; the heading carries the full count and
	// a line says how many more there are.
	MaxQuiet = 20
)

// Quiet is the history the quiet section is built from. Applied is every
// Applied posting's entry (marks.ReadApplied), without notes; Err is the error
// that read returned, in which case the section says it could not be built
// rather than leaving it out, since an unread store is not a store with nothing
// quiet in it.
type Quiet struct {
	Applied map[posting.Key]marks.Entry
	Err     error
}

type quietRow struct {
	key   posting.Key
	stage marks.Stage
	days  int
}

// quietRows are the postings still waiting on an answer (applied, screening or
// interview: an offer is the owner's to answer, and rejected and withdrawn are
// over) whose last change is at least QuietDays whole days before now, longest
// silence first, equal silences by key. A change timestamped after now (a clock
// that ran ahead when it was written) is not quiet: its age is not known to be
// anything.
func quietRows(applied map[posting.Key]marks.Entry, now time.Time) []quietRow {
	var rows []quietRow
	for k, e := range applied {
		if e.Mark != marks.Applied {
			continue
		}
		switch e.Stage {
		case marks.StageNone, marks.StageScreening, marks.StageInterview:
		default:
			continue
		}
		last := e.LastChange()
		if last.IsZero() || last.After(now) {
			continue
		}
		if days := int(now.Sub(last) / (24 * time.Hour)); days >= QuietDays {
			rows = append(rows, quietRow{k, e.Stage, days})
		}
	}
	slices.SortFunc(rows, func(a, b quietRow) int {
		return cmp.Or(cmp.Compare(b.days, a.days), cmp.Compare(a.key, b.key))
	})
	return rows
}

// writeQuiet writes the "Quiet since applied" section: the postings
// quietRows picks, with company and title from the ledger, the stage, the days
// quiet and the key. At most MaxQuiet are listed and the rest are counted. A
// Quiet that could not be read writes a line saying so.
func writeQuiet(b *strings.Builder, recs []ledger.Record, q Quiet, now time.Time) {
	if q.Err != nil {
		b.WriteString("## Quiet since applied\n\n")
		fmt.Fprintf(b, "Not checked: your applied marks and stages could not be read (%s). Applications that have gone quiet are not listed, which does not mean none have.\n\n", Escape(q.Err.Error(), 240))
		return
	}
	rows := quietRows(q.Applied, now)
	if len(rows) == 0 {
		return
	}
	byKey := make(map[posting.Key]ledger.Record, len(rows))
	for _, r := range recs {
		byKey[r.Key] = r
	}
	fmt.Fprintf(b, "## Quiet since applied (%d)\n\n", len(rows))
	fmt.Fprintf(b, "Roles you applied to where nothing has changed for at least %d days, longest first.\n\n", QuietDays)
	for i, row := range rows {
		if i == MaxQuiet {
			fmt.Fprintf(b, "- …and %d more in Jobs (`vani jobs list --view applied`)\n", len(rows)-MaxQuiet)
			break
		}
		title, company := "a posting no longer in the ledger", "unknown company"
		if r, ok := byKey[row.key]; ok {
			title, company = Escape(r.Posting.Title, maxFieldRunes), Escape(r.Posting.Company, maxFieldRunes)
		}
		stage := "applied"
		if row.stage != marks.StageNone {
			stage = string(row.stage)
		}
		fmt.Fprintf(b, "- **%s** at %s: %s, %d days quiet · key `%s`\n", title, company, stage, row.days, codeSpan(string(row.key)))
	}
	b.WriteString("\n")
}
