package eval

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// DescriptionPreviewRunes is how much of the description Label shows.
const DescriptionPreviewRunes = 1500

// maxInputLine bounds one line of the labeller's input.
const maxInputLine = 4096

// Label walks recs interactively: for each it shows the posting and the
// current triage values, then asks each slot in turn (number = option,
// s = skip, q = quit) and writes one label line for the posting. Invalid input
// re-prompts. Quit, or the end of input, stops the session; a posting whose
// slots were not all answered is not written. It returns the number of labels
// written. Posting text is untrusted and is printed as plain text with control
// characters removed; nothing in it is interpreted. Backend names are shown
// as the report files them (see Names).
func Label(in io.Reader, out io.Writer, recs []ledger.Record, s *Store, names Names) (int, error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1024), maxInputLine)
	written := 0
	for i, r := range recs {
		if err := showPosting(out, i+1, len(recs), r, names); err != nil {
			return written, err
		}
		labels := make(map[string]string, len(jdtriage.Slots))
		for _, sl := range jdtriage.Slots {
			choice, quit, err := askSlot(sc, out, sl)
			if err != nil {
				return written, err
			}
			if quit {
				fmt.Fprintf(out, "Stopped; %d label(s) written.\n", written)
				return written, nil
			}
			labels[sl.Name] = choice
		}
		hash, err := s.PutText(r.Posting.Description)
		if err != nil {
			return written, err
		}
		if err := s.Put(Entry{Key: r.Key, Labels: labels, TextSHA256: hash}); err != nil {
			return written, err
		}
		written++
	}
	fmt.Fprintf(out, "Done; %d label(s) written.\n", written)
	return written, nil
}

// askSlot prompts until it has a valid answer. quit is true on "q" or when the
// input ends.
func askSlot(sc *bufio.Scanner, out io.Writer, sl jdtriage.SlotSpec) (choice string, quit bool, err error) {
	fmt.Fprintf(out, "\nSlot %s:\n", sl.Name)
	for i, o := range sl.Options {
		fmt.Fprintf(out, "  %d) %s\n", i+1, o.Value)
	}
	fmt.Fprintln(out, "  s) skip (can't tell)   q) quit")
	for {
		fmt.Fprintf(out, "%s> ", sl.Name)
		if !sc.Scan() {
			if e := sc.Err(); e != nil {
				return "", false, fmt.Errorf("eval: read input: %w", e)
			}
			return "", true, nil
		}
		ans := strings.ToLower(strings.TrimSpace(sc.Text()))
		switch ans {
		case "q", "quit":
			return "", true, nil
		case "s", "skip":
			return Skip, false, nil
		}
		if n, e := strconv.Atoi(ans); e == nil && n >= 1 && n <= len(sl.Options) {
			return sl.Options[n-1].Value, false, nil
		}
		fmt.Fprintf(out, "Not an option; enter 1-%d, s or q.\n", len(sl.Options))
	}
}

func showPosting(out io.Writer, i, n int, r ledger.Record, names Names) error {
	p := r.Posting
	desc := []rune(plain(p.Description))
	shown := desc
	if len(shown) > DescriptionPreviewRunes {
		shown = shown[:DescriptionPreviewRunes]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== %d/%d  %s ===\n", i, n, plain(string(r.Key)))
	fmt.Fprintf(&b, "Title:    %s\nCompany:  %s\nLocation: %s\n", plain(p.Title), plain(p.Company), plain(p.Location))
	fmt.Fprintf(&b, "Description (first %d of %d characters):\n%s\n", len(shown), len(desc), string(shown))
	b.WriteString("Current triage:\n")
	any := false
	for _, sl := range jdtriage.Slots {
		if line := triageLine(r, sl.Name, names); line != "" {
			fmt.Fprintf(&b, "  %s\n", line)
			any = true
		}
	}
	if !any {
		b.WriteString("  (none)\n")
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func triageLine(r ledger.Record, slot string, names Names) string {
	pre := ledger.AnnTriagePrefix + slot
	a := r.Annotations
	val, reading := a[pre], a[pre+ledger.AnnTriageSuffixReading]
	if val == "" && reading == "" {
		return ""
	}
	if val == "" {
		val = "-"
	}
	line := fmt.Sprintf("%s: %s (%s", slot, plain(val), plain(reading))
	if b, _ := names.primary(a[pre+ledger.AnnTriageSuffixBackend]); b != "" {
		line += ", " + plain(b)
	}
	if p := a[pre+ledger.AnnTriageSuffixP]; p != "" {
		line += ", p=" + plain(p)
	}
	if sc := a[pre+ledger.AnnTriageSuffixLappiScore]; sc != "" {
		line += ", lappi_score=" + plain(sc)
	}
	if sh := a[pre+ledger.AnnTriageSuffixShadow]; sh != "" {
		line += ", shadow=" + plain(sh)
	}
	if a[pre+ledger.AnnTriageSuffixDisagree] != "" {
		line += ", DISAGREE"
	}
	return line + ")"
}

// plain drops control characters (terminal escapes included) other than
// newline and tab, so untrusted text cannot drive the terminal.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), r == 0xFFFD:
			return -1
		}
		return r
	}, s)
}
