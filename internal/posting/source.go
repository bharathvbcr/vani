package posting

import "strings"

// Source is where a job board that lists many employers got a posting, for
// display: its name, and the note its terms ask a client to show beside its
// listings. It is the zero Source for an employer's own board (an ATS, a
// careers page or a feed), which needs no attribution.
type Source struct {
	// Name is the board's name as shown ("Dice").
	Name string
	// Note is the board's terms in one line, shown once wherever its rows
	// are; "" when it asks for no more than its name and a link back.
	Note string
}

// The notes the boards' terms ask for (docs/LEGAL.md).
const (
	// diceNote is Dice's AI disclosure: its MCP server's search tool asks a
	// client presenting its results to say they were retrieved with AI.
	diceNote = "Dice listings were retrieved through Dice's AI job-search server; check every detail with the employer before you apply."
	// adzunaNote is Adzuna's attribution for personal use of its API.
	adzunaNote = "Jobs by Adzuna (the Adzuna API, adzuna.com)."
)

// indeedScrape is the token of jobs ingested from Indeed's pages or through
// skills/indeed-to-vani (cmd/vani scrapeToken of www.indeed.com).
const indeedScrape = "www-indeed-com-scrape"

// SourceOf is k's Source: the job board behind a job-board key (hm, ro, zr,
// dc, ad) or an Indeed batch, else the zero Source.
func SourceOf(k Key) Source {
	switch k.ATS() {
	case Himalayas:
		return Source{Name: "Himalayas"}
	case RemoteOK:
		return Source{Name: "Remote OK"}
	case ZipRecruiter:
		return Source{Name: "ZipRecruiter"}
	case Dice:
		return Source{Name: "Dice", Note: diceNote}
	case Adzuna:
		return Source{Name: "Adzuna", Note: adzunaNote}
	case Page:
		if parts := strings.SplitN(string(k), ":", 3); len(parts) == 3 && parts[1] == indeedScrape {
			return Source{Name: "Indeed"}
		}
	}
	return Source{}
}

// SourceNotes is the distinct notes of keys' sources, in a fixed order (the
// order the boards are listed in SourceOf), so a page that shows rows from a
// board that asks for a note shows it once.
func SourceNotes(keys []Key) []string {
	have := map[string]bool{}
	for _, k := range keys {
		if n := SourceOf(k).Note; n != "" {
			have[n] = true
		}
	}
	var out []string
	for _, n := range []string{diceNote, adzunaNote} {
		if have[n] {
			out = append(out, n)
		}
	}
	return out
}
