package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// windowsVersion is WindowsFile's schema version.
const windowsVersion = 1

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
// labels) and each member posting's window.
type windowFile struct {
	V       int                    `json:"v"`
	Windows []windowReg            `json:"windows"`
	Members map[posting.Key]string `json:"members"`
}

type windowReg struct {
	Name string `json:"name"`
	Size int    `json:"size"`
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
// with no members. A file whose registration differs from Windows, or whose
// members name an unregistered or overfull window, is refused: the windows are
// pre-registered and must not drift.
func (s *Store) readWindows() (windowFile, error) {
	fresh := windowFile{V: windowsVersion, Windows: registeredWindows(), Members: map[posting.Key]string{}}
	raw, err := os.ReadFile(s.windowsPath())
	if errors.Is(err, os.ErrNotExist) {
		return fresh, nil
	}
	if err != nil {
		return windowFile{}, fmt.Errorf("eval: read windows: %w", err)
	}
	var wf windowFile
	if err := json.Unmarshal(raw, &wf); err != nil {
		return windowFile{}, fmt.Errorf("eval: windows file unreadable: %w", err)
	}
	if wf.V != windowsVersion {
		return windowFile{}, fmt.Errorf("eval: windows file version %d, want %d", wf.V, windowsVersion)
	}
	if fmt.Sprint(wf.Windows) != fmt.Sprint(registeredWindows()) {
		return windowFile{}, fmt.Errorf("eval: windows file registers %v but the registered windows are %v; windows are pre-registered and cannot change once labelled", wf.Windows, registeredWindows())
	}
	if wf.Members == nil {
		wf.Members = map[posting.Key]string{}
	}
	counts := map[string]int{}
	for k, n := range wf.Members {
		if _, err := LookupWindow(n); err != nil {
			return windowFile{}, fmt.Errorf("eval: windows file files %q: %w", string(k), err)
		}
		counts[n]++
	}
	for _, w := range Windows {
		if counts[w.Name] > w.Size {
			return windowFile{}, fmt.Errorf("eval: window %q holds %d postings, over its size %d", w.Name, counts[w.Name], w.Size)
		}
	}
	return wf, nil
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
	return wf.Members, nil
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

// SyncWindows files every labelled posting the shadow was asked about, and
// that is not yet in a window, into the first window that is not closed, in
// label-time order (then key order). A filed posting never moves, so windows
// stay disjoint however often this runs. Only a posting with a non-skip
// triage label and a shadow reading on its ledger record counts: a label on
// a posting the shadow was not asked about says nothing about the shadow.
// It is idempotent and returns how many postings it filed.
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
	byKey := make(map[posting.Key]ledger.Record, len(recs))
	for _, r := range recs {
		byKey[r.Key] = r
	}
	var cand []Entry
	for k, e := range triageLabels(labels) {
		if _, filed := wf.Members[k]; filed || !hasNonSkip(e) {
			continue
		}
		if r, ok := byKey[k]; ok && !Inherited(r) && hasShadowReading(r) {
			cand = append(cand, e)
		}
	}
	sort.Slice(cand, func(i, j int) bool {
		if !cand[i].LabeledAt.Equal(cand[j].LabeledAt) {
			return cand[i].LabeledAt.Before(cand[j].LabeledAt)
		}
		return cand[i].Key < cand[j].Key
	})
	filed := 0
	for _, e := range cand {
		w, ok := openWindow(wf.Members)
		if !ok {
			break
		}
		wf.Members[e.Key] = w.Name
		filed++
	}
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
}

// SlotVerdict is the per-slot promotion decision over the registered windows.
type SlotVerdict struct {
	Slot, Shadow, Primary string
	Windows               []WindowVerdict
	// Promotable is true only when every registered window is closed and says
	// "promotable" for this row.
	Promotable bool
	Why        string
}

// WindowReport is the per-window Promotion tables and the per-slot verdicts.
type WindowReport struct {
	Windows      []Window
	Members      map[string]int
	Tables       map[string][]Promotion
	SlotVerdicts []SlotVerdict
}

// BuildWindows computes each registered window's Promotion rows over its
// member postings only (with the all-labels arithmetic and MinPromotionLabels)
// and combines them per slot: a row is promotable only if every window is
// closed and Promotion.Verdict says "promotable" in each. It does not file new
// members; call SyncWindows first.
func BuildWindows(recs []ledger.Record, s *Store, names Names) (WindowReport, error) {
	labels, bad, err := s.Labels()
	if err != nil {
		return WindowReport{}, fmt.Errorf("eval: windows need the labels: %w", err)
	}
	if bad > 0 {
		return WindowReport{}, fmt.Errorf("eval: %d label lines are unreadable; refusing a window verdict over a partial label file", bad)
	}
	members, err := s.WindowMembers()
	if err != nil {
		return WindowReport{}, err
	}
	labels = triageLabels(labels)
	rep := WindowReport{Windows: append([]Window(nil), Windows...), Members: map[string]int{}, Tables: map[string][]Promotion{}}
	type rowKey struct{ slot, shadow, primary string }
	rows := map[rowKey]map[string]Promotion{}
	for _, w := range Windows {
		sub := map[posting.Key]Entry{}
		for k, n := range members {
			if n == w.Name {
				if e, ok := labels[k]; ok {
					sub[k] = e
				}
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
			if p, ok := rows[k][w.Name]; ok {
				wv.Present, wv.Verdict = true, p.Verdict()
			} else {
				wv.Verdict = "no labelled posting with a shadow reading for this slot"
			}
			sv.Windows = append(sv.Windows, wv)
			switch {
			case !wv.Closed:
				sv.Promotable = false
				why = append(why, fmt.Sprintf("%s not closed (%d/%d postings)", w.Name, wv.Members, w.Size))
			case wv.Verdict != "promotable":
				sv.Promotable = false
				why = append(why, fmt.Sprintf("%s: %s", w.Name, wv.Verdict))
			}
		}
		if sv.Promotable {
			sv.Why = "promotable in every window"
		} else {
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
	b.WriteString("Windows are disjoint: a labelled, shadow-asked posting is filed in the first window not yet closed and never moves. A slot is promotable only if every window is closed and says promotable.\n")
	for _, w := range r.Windows {
		state := "open"
		if r.Members[w.Name] >= w.Size {
			state = "closed"
		}
		fmt.Fprintf(&b, "\n### %s: %d/%d postings (%s)\n\n", w.Name, r.Members[w.Name], w.Size, state)
		if ps := r.Tables[w.Name]; len(ps) == 0 {
			b.WriteString("No member posting has a shadow reading on a labelled slot: n/a\n")
		} else {
			writePromotions(&b, ps, MinPromotionLabels)
		}
	}
	b.WriteString("\n### Per-slot verdict\n\n")
	if len(r.SlotVerdicts) == 0 {
		b.WriteString("No slot has a labelled shadow reading in any window: n/a\n")
	}
	for _, v := range r.SlotVerdicts {
		verdict := "refused"
		if v.Promotable {
			verdict = "promotable"
		}
		fmt.Fprintf(&b, "- %s / %q vs %q: %s (%s)\n", v.Slot, v.Shadow, v.Primary, verdict, v.Why)
		for _, w := range v.Windows {
			fmt.Fprintf(&b, "  - %s: %s\n", w.Window, w.Verdict)
		}
	}
	return b.String()
}
