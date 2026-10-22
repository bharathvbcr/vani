package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Window is a registered label window: a fixed number of labelled postings,
// each of which the shadow was asked about. A window is closed once it holds
// Size postings, and a posting belongs to at most one window, so two windows
// are disjoint evidence for the same promotion question (card ft-7099bd3b,
// criterion 6). Like Split, a window is chosen by registration name, never by
// a free parameter.
type Window struct {
	Name string
	Size int
}

// Windows are the registered label windows in fill order: a label is filed in
// the first window that is not yet closed.
var Windows = []Window{
	{Name: "window-1", Size: 100},
	{Name: "window-2", Size: 100},
}

// WindowsFile is the window registration and membership file inside the label
// directory.
const WindowsFile = "windows.json"

// windowsVersion is WindowsFile's schema version. Version 2 freezes each
// member's labels at filing time; version 1 (members as bare window names)
// had no frozen labels and is refused rather than guessed at.
const windowsVersion = 2

// WindowNames lists the registered window names in fill order.
func WindowNames() []string {
	out := make([]string, len(Windows))
	for i, w := range Windows {
		out[i] = w.Name
	}
	return out
}

// LookupWindow returns the registered window named name.
func LookupWindow(name string) (Window, error) {
	for _, w := range Windows {
		if w.Name == name {
			return w, nil
		}
	}
	return Window{}, fmt.Errorf("unknown label window %q; registered: %s", name, strings.Join(WindowNames(), ", "))
}

// windowFile is the persisted state: the registration (so a later change to
// Windows cannot silently resize or rename a window that already holds
// labels) and each member posting's window and frozen labels.
type windowFile struct {
	V       int                          `json:"v"`
	Windows []windowReg                  `json:"windows"`
	Members map[posting.Key]windowMember `json:"members"`
}

type windowReg struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

// windowMember is one filed posting: its window and its triage labels as they
// stood when it was filed. The window verdict is computed from these, never
// from the live label log, so editing a label after a window closed cannot
// move that window's verdict. LabelSHA256 is the identity of the label line
// (key, labeled_at and the label values), checked on every read, so an edit to
// windows.json that does not also forge the hash is refused.
type windowMember struct {
	Window      string            `json:"window"`
	LabeledAt   time.Time         `json:"labeled_at"`
	Labels      map[string]string `json:"labels"`
	LabelSHA256 string            `json:"label_sha256"`
}

// labelIdentity hashes the triage-relevant content of a label line.
func labelIdentity(key posting.Key, at time.Time, labels map[string]string) string {
	slots := make([]string, 0, len(labels))
	for slot := range labels {
		if slot != WouldApply {
			slots = append(slots, slot)
		}
	}
	sort.Strings(slots)
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", string(key), at.UTC().Format(time.RFC3339Nano))
	for _, slot := range slots {
		fmt.Fprintf(h, "%s=%s\n", slot, labels[slot])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newMember(window string, e Entry) windowMember {
	labels := make(map[string]string, len(e.Labels))
	for slot, v := range e.Labels {
		if slot != WouldApply {
			labels[slot] = v
		}
	}
	return windowMember{Window: window, LabeledAt: e.LabeledAt, Labels: labels, LabelSHA256: labelIdentity(e.Key, e.LabeledAt, labels)}
}

func registeredWindows() []windowReg {
	out := make([]windowReg, len(Windows))
	for i, w := range Windows {
		out[i] = windowReg{w.Name, w.Size}
	}
	return out
}

func (s *Store) windowsPath() string { return filepath.Join(s.labelDir, WindowsFile) }

// readWindows reads the persisted state; a missing file is the registration
// with no members. A file with an unknown field, a registration that differs
// from Windows, members naming an unregistered or overfull window, or a
// member whose frozen labels do not match their hash, is refused: the windows
// are pre-registered and must not drift, and a file this version does not
// fully understand must not be rewritten.
func (s *Store) readWindows() (windowFile, error) {
	fresh := windowFile{V: windowsVersion, Windows: registeredWindows(), Members: map[posting.Key]windowMember{}}
	raw, err := os.ReadFile(s.windowsPath())
	if errors.Is(err, os.ErrNotExist) {
		return fresh, nil
	}
	if err != nil {
		return windowFile{}, fmt.Errorf("eval: read windows: %w", err)
	}
	var head struct {
		V int `json:"v"`
	}
	if json.Unmarshal(raw, &head) == nil && head.V != windowsVersion {
		return windowFile{}, fmt.Errorf("eval: windows file version %d, want %d", head.V, windowsVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wf windowFile
	if err := dec.Decode(&wf); err != nil {
		return windowFile{}, fmt.Errorf("eval: windows file unreadable or has fields this version does not know (refusing to rewrite it): %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return windowFile{}, errors.New("eval: windows file has data after the document")
	}
	if wf.V != windowsVersion {
		return windowFile{}, fmt.Errorf("eval: windows file version %d, want %d", wf.V, windowsVersion)
	}
	if fmt.Sprint(wf.Windows) != fmt.Sprint(registeredWindows()) {
		return windowFile{}, fmt.Errorf("eval: windows file registers %v but the registered windows are %v; windows are pre-registered and cannot change once labelled", wf.Windows, registeredWindows())
	}
	if wf.Members == nil {
		wf.Members = map[posting.Key]windowMember{}
	}
	counts := map[string]int{}
	for k, m := range wf.Members {
		if _, err := LookupWindow(m.Window); err != nil {
			return windowFile{}, fmt.Errorf("eval: windows file files %q: %w", string(k), err)
		}
		if m.LabeledAt.IsZero() || len(m.Labels) == 0 || m.LabelSHA256 != labelIdentity(k, m.LabeledAt, m.Labels) {
			return windowFile{}, fmt.Errorf("eval: windows file member %q does not match its label hash; the file was edited", string(k))
		}
		counts[m.Window]++
	}
	for _, w := range Windows {
		if counts[w.Name] > w.Size {
			return windowFile{}, fmt.Errorf("eval: window %q holds %d postings, over its size %d", w.Name, counts[w.Name], w.Size)
		}
	}
	return wf, nil
}

// requireLabels refuses a window file that has members when the label log is
// gone: counting those windows over no labels would report them closed with
// nothing in them.
func (s *Store) requireLabels(wf windowFile) error {
	if len(wf.Members) == 0 {
		return nil
	}
	if _, err := os.Stat(s.labelsPath()); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("eval: %s holds %d members but %s is missing; restore the label log before computing a window verdict", WindowsFile, len(wf.Members), LabelsFile)
	}
	return nil
}

// WindowMembers returns each member posting's window, as persisted.
func (s *Store) WindowMembers() (map[posting.Key]string, error) {
	if s == nil {
		return nil, errors.New("eval: no label store")
	}
	wf, err := s.readWindows()
	if err != nil {
		return nil, err
	}
	return memberWindows(wf.Members), nil
}

func memberWindows(m map[posting.Key]windowMember) map[posting.Key]string {
	out := make(map[posting.Key]string, len(m))
	for k, v := range m {
		out[k] = v.Window
	}
	return out
}

// OpenWindow returns the window the next label would be filed in: the first
// registered window not yet closed. ok is false when every window is closed.
func (s *Store) OpenWindow() (w Window, ok bool, err error) {
	m, err := s.WindowMembers()
	if err != nil {
		return Window{}, false, err
	}
	w, ok = openWindow(m)
	return w, ok, nil
}

func openWindow(members map[posting.Key]string) (Window, bool) {
	counts := map[string]int{}
	for _, n := range members {
		counts[n]++
	}
	for _, w := range Windows {
		if counts[w.Name] < w.Size {
			return w, true
		}
	}
	return Window{}, false
}

// WindowFree is how many more postings window w takes.
func (s *Store) WindowFree(w Window) (int, error) {
	m, err := s.WindowMembers()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range m {
		if name == w.Name {
			n++
		}
	}
	return max(w.Size-n, 0), nil
}

// fileWindows files, in memory, every labelled posting the shadow was asked
// about and that is not yet in a window, into the first window that is not
// closed, in label-time order (then key order). It returns how many it filed
// and how many it refused as late.
//
// A posting is eligible only if its shadow reading existed when it was
// labelled: labels are for shadow-asked postings (card criterion 4). The
// ledger does not date a reading, so the test is order: a posting labelled
// before a posting that is already filed was labelled before the shadow read
// it (it would have been filed with its neighbours otherwise), and is never
// filed. That keeps the windows disjoint in label time too: every member of
// a later window was labelled at or after every member of an earlier one.
func fileWindows(wf windowFile, labels map[posting.Key]Entry, recs []ledger.Record) (filed, late int) {
	var watermark time.Time
	for _, m := range wf.Members {
		if m.LabeledAt.After(watermark) {
			watermark = m.LabeledAt
		}
	}
	byKey := make(map[posting.Key]ledger.Record, len(recs))
	for _, r := range recs {
		byKey[r.Key] = r
	}
	var cand []Entry
	for k, e := range triageLabels(labels) {
		if _, f := wf.Members[k]; f || !hasNonSkip(e) {
			continue
		}
		r, ok := byKey[k]
		if !ok || Inherited(r) || !hasShadowReading(r) {
			continue
		}
		if e.LabeledAt.Before(watermark) {
			late++
			continue
		}
		cand = append(cand, e)
	}
	sort.Slice(cand, func(i, j int) bool {
		if !cand[i].LabeledAt.Equal(cand[j].LabeledAt) {
			return cand[i].LabeledAt.Before(cand[j].LabeledAt)
		}
		return cand[i].Key < cand[j].Key
	})
	for _, e := range cand {
		w, ok := openWindow(memberWindows(wf.Members))
		if !ok {
			break
		}
		wf.Members[e.Key] = newMember(w.Name, e)
		filed++
	}
	return filed, late
}

// SyncWindows files labelled postings into windows (see fileWindows) and
// persists windows.json. It is the one writer of that file: `vani label
// --window` calls it, and nothing that only reports does. A filed posting
// never moves and its labels are frozen as filed, so windows stay disjoint and
// their verdicts stay put however often this runs. Only a posting with a
// non-skip triage label and a shadow reading on its ledger record counts. It
// is idempotent and returns how many postings it filed.
func (s *Store) SyncWindows(recs []ledger.Record) (int, error) {
	if s == nil {
		return 0, errors.New("eval: no label store")
	}
	unlock, err := s.lockLabels()
	if err != nil {
		return 0, err
	}
	defer unlock()
	labels, _, err := s.Labels()
	if err != nil {
		return 0, err
	}
	wf, err := s.readWindows()
	if err != nil {
		return 0, err
	}
	if err := s.requireLabels(wf); err != nil {
		return 0, err
	}
	filed, _ := fileWindows(wf, labels, recs)
	if _, err := os.Stat(s.windowsPath()); filed == 0 && err == nil {
		return 0, nil
	}
	raw, err := json.Marshal(wf)
	if err != nil {
		return 0, err
	}
	if err := atomicfile.Write(s.windowsPath(), append(raw, '\n')); err != nil {
		return 0, fmt.Errorf("eval: write windows: %w", err)
	}
	return filed, nil
}

func hasNonSkip(e Entry) bool {
	for slot, v := range e.Labels {
		if slot != WouldApply && v != "" && v != Skip {
			return true
		}
	}
	return false
}

// WindowVerdict is one window's Promotion verdict for one slot row.
type WindowVerdict struct {
	Window  string
	Members int
	Size    int
	Closed  bool
	Present bool   // the window has a Promotion row for this slot, shadow and primary
	Verdict string // Promotion.Verdict, or why there is none
	// Final is true when the window is closed and its verdict is not
	// promotable: a closed window never grows, so no later label changes it.
	Final bool
}

// SlotVerdict is the per-slot promotion decision over the registered windows.
type SlotVerdict struct {
	Slot, Shadow, Primary string
	Windows               []WindowVerdict
	// Promotable is true only when every registered window is closed and says
	// "promotable" for this row.
	Promotable bool
	// Final is true when some closed window already refuses this row: the slot
	// cannot be promoted from these windows, however the others fill.
	Final bool
	Why   string
}

// WindowReport is the per-window Promotion tables and the per-slot verdicts.
type WindowReport struct {
	Windows []Window
	Members map[string]int
	Tables  map[string][]Promotion
	// Changed counts, per window, members whose live label no longer matches
	// the label filed (edited, or gone from the label log). The tables and
	// verdicts use the labels as filed.
	Changed map[string]int
	// Late counts labelled, shadow-asked postings that were refused a window
	// because they were labelled before a posting already filed.
	Late         int
	SlotVerdicts []SlotVerdict
}

// BuildWindows computes each registered window's Promotion rows over its
// member postings' frozen labels (with the all-labels arithmetic and
// MinPromotionLabels) and combines them per slot: a row is promotable only if
// every window is closed and Promotion.Verdict says "promotable" in each. It
// never writes: postings not yet filed are filed in memory for the report
// (what SyncWindows would persist), and windows.json is left as it was.
func BuildWindows(recs []ledger.Record, s *Store, names Names) (WindowReport, error) {
	labels, bad, err := s.Labels()
	if err != nil {
		return WindowReport{}, fmt.Errorf("eval: windows need the labels: %w", err)
	}
	if bad > 0 {
		return WindowReport{}, fmt.Errorf("eval: %d label lines are unreadable; refusing a window verdict over a partial label file", bad)
	}
	wf, err := s.readWindows()
	if err != nil {
		return WindowReport{}, err
	}
	if err := s.requireLabels(wf); err != nil {
		return WindowReport{}, err
	}
	_, late := fileWindows(wf, labels, recs)
	live := triageLabels(labels)
	rep := WindowReport{
		Windows: append([]Window(nil), Windows...),
		Members: map[string]int{}, Tables: map[string][]Promotion{}, Changed: map[string]int{}, Late: late,
	}
	type rowKey struct{ slot, shadow, primary string }
	rows := map[rowKey]map[string]Promotion{}
	for _, w := range Windows {
		sub := map[posting.Key]Entry{}
		for k, m := range wf.Members {
			if m.Window != w.Name {
				continue
			}
			sub[k] = Entry{V: SchemaVersion, Key: k, Labels: m.Labels, LabeledAt: m.LabeledAt}
			if e, ok := live[k]; !ok || labelIdentity(k, e.LabeledAt, e.Labels) != m.LabelSHA256 {
				rep.Changed[w.Name]++
			}
		}
		rep.Members[w.Name] = len(sub)
		ps := buildReport(recs, sub, 0, nil, names).Promotions
		rep.Tables[w.Name] = ps
		for _, p := range ps {
			k := rowKey{p.Slot, p.Shadow, p.Primary}
			if rows[k] == nil {
				rows[k] = map[string]Promotion{}
			}
			rows[k][w.Name] = p
		}
	}
	keys := make([]rowKey, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.slot != b.slot {
			return a.slot < b.slot
		}
		if a.shadow != b.shadow {
			return a.shadow < b.shadow
		}
		return a.primary < b.primary
	})
	for _, k := range keys {
		sv := SlotVerdict{Slot: k.slot, Shadow: k.shadow, Primary: k.primary, Promotable: true}
		var why []string
		for _, w := range Windows {
			wv := WindowVerdict{Window: w.Name, Members: rep.Members[w.Name], Size: w.Size, Closed: rep.Members[w.Name] >= w.Size}
			p, present := rows[k][w.Name]
			if present {
				wv.Present, wv.Verdict = true, p.Verdict()
			} else {
				wv.Verdict = "no labelled posting with a shadow reading for this slot"
			}
			switch {
			case !wv.Closed:
				sv.Promotable = false
				why = append(why, fmt.Sprintf("%s not closed (%d/%d postings)", w.Name, wv.Members, w.Size))
			case !present:
				wv.Final, sv.Promotable, sv.Final = true, false, true
				wv.Verdict = "cannot promote: window closed with no labelled posting for this slot"
				why = append(why, w.Name+": "+wv.Verdict)
			case wv.Verdict != "promotable":
				wv.Final, sv.Promotable, sv.Final = true, false, true
				if strings.HasPrefix(wv.Verdict, "insufficient disagreements") {
					wv.Verdict = fmt.Sprintf("cannot promote: window closed with %d discordant (needs %d)", p.Discordant(), MinDiscordant)
				} else {
					wv.Verdict = "cannot promote: window closed, " + wv.Verdict
				}
				why = append(why, w.Name+": "+wv.Verdict)
			}
			sv.Windows = append(sv.Windows, wv)
		}
		switch {
		case sv.Promotable:
			sv.Why = "promotable in every window"
		case sv.Final:
			sv.Why = strings.Join(why, "; ")
		default:
			sv.Why = "not promotable: " + strings.Join(why, "; ")
		}
		rep.SlotVerdicts = append(rep.SlotVerdicts, sv)
	}
	return rep, nil
}

// Markdown renders each window's Promotion table and the per-slot verdicts.
func (r WindowReport) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Label windows\n\n")
	b.WriteString("Windows are disjoint: a labelled, shadow-asked posting is filed in the first window not yet closed and never moves, and its labels are frozen as filed. A slot is promotable only if every window is closed and says promotable.\n")
	for _, w := range r.Windows {
		state := "open"
		if r.Members[w.Name] >= w.Size {
			state = "closed"
		}
		fmt.Fprintf(&b, "\n### %s: %d/%d postings (%s)\n\n", w.Name, r.Members[w.Name], w.Size, state)
		if n := r.Changed[w.Name]; n > 0 {
			fmt.Fprintf(&b, "%d labels changed after %s was filed; verdict uses the labels as filed\n\n", n, w.Name)
		}
		if ps := r.Tables[w.Name]; len(ps) == 0 {
			b.WriteString("No member posting has a shadow reading on a labelled slot: n/a\n")
		} else {
			writePromotions(&b, ps, MinPromotionLabels)
		}
	}
	if r.Late > 0 {
		fmt.Fprintf(&b, "\n%d labelled postings were not filed in any window: they were labelled before a posting already filed, so before the shadow read them\n", r.Late)
	}
	b.WriteString("\n### Per-slot verdict\n\n")
	if len(r.SlotVerdicts) == 0 {
		b.WriteString("No slot has a labelled shadow reading in any window: n/a\n")
	}
	for _, v := range r.SlotVerdicts {
		verdict := "refused"
		switch {
		case v.Promotable:
			verdict = "promotable"
		case v.Final:
			verdict = "cannot promote"
		}
		fmt.Fprintf(&b, "- %s / %q vs %q: %s (%s)\n", v.Slot, v.Shadow, v.Primary, verdict, v.Why)
		for _, w := range v.Windows {
			fmt.Fprintf(&b, "  - %s: %s\n", w.Window, w.Verdict)
		}
	}
	return b.String()
}
