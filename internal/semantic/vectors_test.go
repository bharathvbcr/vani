package semantic

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// ---- helpers ----

// vecDocs returns n distinct postings whose chunk counts vary.
func vecDocs(n int) []Doc {
	topics := []string{
		"Operate k8s clusters in production environments",
		"Bake fresh pastry and croissants every morning",
		"Build golang backend services for developer tooling",
		"Design postgres schemas for reporting workloads",
		"Decorate wedding cakes for demanding customers",
		"Write CI pipelines and deployment automation",
	}
	docs := make([]Doc, n)
	for i := range docs {
		desc := "Requirements:"
		for k := 0; k < 2+i%5; k++ {
			desc += fmt.Sprintf("\n- %s number %d", topics[(i+k)%len(topics)], i*7+k)
		}
		docs[i] = Doc{Key: fmt.Sprintf("doc%03d", i), Title: fmt.Sprintf("Engineer %d", i), Description: desc}
	}
	return docs
}

func hugeDoc() Doc {
	desc := "Requirements:"
	for i := 0; i < 60; i++ {
		desc += fmt.Sprintf("\n- Operate service%d with golang tooling and kubernetes rollout%d", i, i*31)
	}
	return Doc{Key: "huge", Title: "Staff Engineer", Description: desc}
}

func profile2(t testing.TB) Profile {
	t.Helper()
	p := synthProfile(t)
	p.Chunks = append(p.Chunks, Chunk{Label: "skills", Text: "Skills: Haskell, OCaml", Weight: 1})
	return p
}

func profile3(t testing.TB) Profile {
	t.Helper()
	p := profile2(t)
	p.Target += " and compilers"
	return p
}

func hashOf(d Doc) [16]byte {
	h, ok := parseTextHash(TextHash(d.Title, d.Description))
	if !ok {
		panic("TextHash is not 16 hex bytes")
	}
	return h
}

func warmIn(t *testing.T, e Embedder, p Profile, dir string, docs []Doc) (*Matcher, Report) {
	t.Helper()
	m, err := NewMatcher(e, p, dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Warm(context.Background(), docs, 1000)
	if err != nil {
		t.Fatalf("Warm: %v (%+v)", err, rep)
	}
	return m, rep
}

func matchesOf(t *testing.T, m *Matcher, docs []Doc) map[string]Match {
	t.Helper()
	out := map[string]Match{}
	for _, d := range docs {
		mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
		if !ok {
			t.Fatalf("%s has no score", d.Key)
		}
		out[d.Key] = mt
	}
	return out
}

// oracle scores docs against p from nothing, embedding every posting.
func oracle(t *testing.T, p Profile, docs []Doc) map[string]Match {
	t.Helper()
	m, rep := warmIn(t, newSpy(), p, filepath.Join(t.TempDir(), "oracle"), docs)
	if rep.Scored != len(docs) || rep.Rescored != 0 {
		t.Fatalf("oracle did not embed everything: %+v", rep)
	}
	return matchesOf(t, m, docs)
}

func assertSame(t *testing.T, got, want map[string]Match) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d matches, want %d", len(got), len(want))
	}
	for k, w := range want {
		if g := got[k]; !reflect.DeepEqual(g, w) {
			t.Errorf("%s: %+v != fresh embed %+v", k, g, w)
		}
	}
}

// covered checks the Report invariant: every distinct posting is in exactly one
// bucket.
func covered(t *testing.T, rep Report, n int) {
	t.Helper()
	if got := rep.Scored + rep.Cached + rep.Rescored + rep.Failed + rep.Skipped; got != n {
		t.Errorf("report covers %d of %d postings: %+v", got, n, rep)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// readLog splits the log into its header and records (each with its length prefix).
func readLog(t *testing.T, dir string) (hdr []byte, recs [][]byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, vectorLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	mlen := int(binary.LittleEndian.Uint32(raw[len(vecMagic)+16:]))
	hl := vecHdrFixed + mlen
	hdr = raw[:hl]
	for off := hl; off < len(raw); {
		ln := int(binary.LittleEndian.Uint32(raw[off:]))
		recs = append(recs, raw[off:off+4+ln])
		off += 4 + ln
	}
	return hdr, recs
}

func writeLog(t *testing.T, dir string, hdr []byte, recs ...[]byte) {
	t.Helper()
	var b []byte
	b = append(b, hdr...)
	for _, r := range recs {
		b = append(b, r...)
	}
	if err := os.WriteFile(filepath.Join(dir, vectorLogFileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func logPath(dir string) string { return filepath.Join(dir, vectorLogFileName) }

// craftRecord frames a record of any shape, with a correct checksum.
func craftRecord(h [16]byte, n, dim int, fill func(i, k int) float32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(recFixed+4*n*dim+4))
	b = append(b, h[:]...)
	b = binary.LittleEndian.AppendUint16(b, uint16(n))
	b = binary.LittleEndian.AppendUint16(b, uint16(dim))
	for i := 0; i < n; i++ {
		for k := 0; k < dim; k++ {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(fill(i, k)))
		}
	}
	return binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

func oneHot(i, k int) float32 {
	if k == i%8 {
		return 1
	}
	return 0
}

func recIndex(t *testing.T, recs [][]byte, d Doc) int {
	t.Helper()
	h := hashOf(d)
	for i, r := range recs {
		if string(r[4:20]) == string(h[:]) {
			return i
		}
	}
	t.Fatalf("no record for %s", d.Key)
	return -1
}

func nChunks(d Doc) int { return len(ChunkPosting(d.Title, d.Description)) }

// ---- the claim: a profile change rescores from vectors ----

func TestRescoreParityOracle(t *testing.T) {
	docs := append(vecDocs(24), hugeDoc(), Doc{Key: "title", Title: "Kubernetes Engineer"})
	dir := filepath.Join(t.TempDir(), "cache")
	_, rep := warmIn(t, newSpy(), synthProfile(t), dir, docs)
	if rep.Scored != len(docs) || rep.VectorsAppended != len(docs) || rep.VectorsErr != "" {
		t.Fatalf("first warm: %+v", rep)
	}
	if n := nChunks(hugeDoc()); n < 10 {
		t.Fatalf("huge posting cut into %d chunks; it should exercise the dedup and top-m paths", n)
	}

	// Same directory state for both arms (profile file, scores) except the
	// vectors, so only the provenance of the posting vectors differs.
	dir2 := filepath.Join(t.TempDir(), "cache2")
	copyDir(t, dir, dir2)
	if err := os.Remove(logPath(dir2)); err != nil {
		t.Fatal(err)
	}
	p2 := profile2(t)
	mA, repA := warmIn(t, newSpy(), p2, dir, docs)
	mB, repB := warmIn(t, newSpy(), p2, dir2, docs)
	if repA.Rescored != len(docs) || repA.Scored != 0 || repA.Cached != 0 {
		t.Fatalf("rescore arm did not rescore: %+v", repA)
	}
	if repB.Scored != len(docs) || repB.Rescored != 0 {
		t.Fatalf("fresh arm did not embed: %+v", repB)
	}
	a, b := matchesOf(t, mA, docs), matchesOf(t, mB, docs)
	assertSame(t, a, b) // exact ==, no tolerance
	var thin, full int
	for _, mt := range a {
		if mt.Thin {
			thin++
		}
		if len(mt.Evidence) == evidenceN {
			full++
		}
	}
	if thin == 0 || full == 0 {
		t.Errorf("fixture too tame: %d thin, %d with full evidence", thin, full)
	}
	// The profile changed, so the scores are not the old ones.
	old := oracle(t, synthProfile(t), docs)
	if reflect.DeepEqual(old, a) {
		t.Error("p1 and p2 scores are identical; the profile change tested nothing")
	}
}

func TestProfileChangeRescoresFromVectors(t *testing.T) {
	docs := vecDocs(30)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)

	s2 := newSpy()
	p2 := profile2(t)
	_, rep := warmIn(t, s2, p2, dir, docs)
	if rep.Rescored != 30 || rep.Scored != 0 || rep.Failed != 0 {
		t.Fatalf("%+v", rep)
	}
	covered(t, rep, 30)
	allowed := map[string]bool{}
	for _, c := range p2.Chunks {
		allowed[c.Text] = true
	}
	for _, a := range offDomainAnchors {
		for _, c := range ChunkPosting(a.title, a.desc) {
			allowed[c.Text] = true
		}
	}
	for _, c := range ChunkPosting(p2.Target, "") {
		allowed[c.Text] = true
	}
	if s2.count() == 0 {
		t.Fatal("the new profile was never embedded")
	}
	for _, txt := range s2.texts {
		if !allowed[txt] {
			t.Fatalf("the embedder saw a posting text on a rescore: %q", txt)
		}
	}
}

func TestRescoreDoesNotCountAgainstMax(t *testing.T) {
	docs := vecDocs(50)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	m, err := NewMatcher(newSpy(), profile2(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Warm(context.Background(), docs, 1)
	if err != nil || rep.Rescored != 50 || rep.Scored != 0 || rep.Skipped != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	extra := vecDocs(53)[50:]
	rep, err = m.Warm(context.Background(), append(append([]Doc(nil), docs...), extra...), 1)
	if err != nil || rep.Cached != 50 || rep.Scored != 1 || rep.Skipped != 2 {
		t.Fatalf("new postings still obey max: %+v %v", rep, err)
	}
	covered(t, rep, 53)
	// max 0 still rescores.
	m3, err := NewMatcher(newSpy(), profile3(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err = m3.Warm(context.Background(), docs, 0)
	if err != nil || rep.Rescored != 50 {
		t.Fatalf("max 0: %+v %v", rep, err)
	}
}

func TestOneTextUnderSeveralKeysEmbedsOnce(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	a := k8sDoc()
	mustWarm(t, m, a)
	before := len(s.texts)
	b := a
	b.Key = "same-text-other-key"
	rep := mustWarm(t, m, b)
	if rep.Rescored != 1 || rep.Scored != 0 || len(s.texts) != before {
		t.Fatalf("%+v; embedder saw %d new texts", rep, len(s.texts)-before)
	}
	if !reflect.DeepEqual(lookup(t, m, a), lookup(t, m, b)) {
		t.Error("the same text scored differently under two keys")
	}
}

func TestRescoreModelUnavailableNeverScoresZero(t *testing.T) {
	docs := vecDocs(5)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	down := newSpy()
	down.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, ErrUnavailable, true
	}
	m, err := NewMatcher(down, profile2(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Warm(context.Background(), docs, 10)
	if !errors.Is(err, ErrUnavailable) || !rep.Unavailable || rep.Rescored != 0 || rep.Failed != 5 || m.Len() != 0 {
		t.Fatalf("%+v %v len=%d", rep, err, m.Len())
	}
	covered(t, rep, 5)
}

// ---- file lifecycle ----

func TestVectorsVersionOrModelIgnored(t *testing.T) {
	docs := vecDocs(6)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)

	// Another ChunkVersion: the vectors were chunked differently, so none is trusted.
	f, err := os.OpenFile(logPath(dir), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(binary.LittleEndian.AppendUint32(nil, ChunkVersion+1), int64(len(vecMagic)+4)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, err := VectorStatus(dir)
	if err != nil || before.Version != ChunkVersion+1 {
		t.Fatalf("%+v %v", before, err)
	}
	m, rep := warmIn(t, newSpy(), profile2(t), dir, docs)
	if rep.Rescored != 0 || rep.Scored != 6 || rep.VectorsRebuilt || rep.VectorsAppended != 6 {
		t.Fatalf("version: %+v", rep)
	}
	assertSame(t, matchesOf(t, m, docs), oracle(t, profile2(t), docs))
	st, err := VectorStatus(dir)
	if err != nil || st.Version != ChunkVersion || st.Records != 6 || st.Generation == before.Generation {
		t.Fatalf("log not replaced: %+v %v", st, err)
	}

	// Another model: replaced as well, and the new log serves the new model.
	other := &spy{inner: NewFakeEmbedder(256, synonyms), model: "other-model/dim=256"}
	_, rep = warmIn(t, other, profile3(t), dir, docs)
	if rep.Rescored != 0 || rep.Scored != 6 {
		t.Fatalf("model: %+v", rep)
	}
	if st, err = VectorStatus(dir); err != nil || st.Model != "other-model/dim=256" || st.Records != 6 {
		t.Fatalf("%+v %v", st, err)
	}
	_, rep = warmIn(t, &spy{inner: NewFakeEmbedder(256, synonyms), model: "other-model/dim=256"}, synthProfile(t), dir, docs)
	if rep.Rescored != 6 {
		t.Fatalf("the replaced log is not usable by its own model: %+v", rep)
	}
}

// TestVectorsIgnoreTheScoringVersion: the log is keyed by how postings are
// chunked (ChunkVersion), not by how they are scored (Version). A log written
// before a scoring-only bump (its header holds the number below) is rescored
// from, not replaced: replacing it would re-embed every posting for a change
// that touched no chunk.
func TestVectorsIgnoreTheScoringVersion(t *testing.T) {
	// 3 is what every log on disk before the split holds. Bumping ChunkVersion
	// re-embeds each posting once; do it only when ChunkPosting changes, and
	// change this number with it.
	const writtenBeforeTheSplit = 3
	if ChunkVersion != writtenBeforeTheSplit {
		t.Fatalf("ChunkVersion is %d: every existing vector log would be replaced and every posting re-embedded", ChunkVersion)
	}
	if Version <= ChunkVersion {
		t.Fatalf("Version %d should be ahead of ChunkVersion %d once a scoring change has shipped without a chunking one", Version, ChunkVersion)
	}
	docs := vecDocs(6)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	before, err := VectorStatus(dir)
	if err != nil || before.Version != ChunkVersion || before.Records != 6 {
		t.Fatalf("a fresh log is not stamped with ChunkVersion: %+v %v", before, err)
	}
	// An older binary's log: its header holds the literal.
	f, err := os.OpenFile(logPath(dir), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(binary.LittleEndian.AppendUint32(nil, writtenBeforeTheSplit), int64(len(vecMagic)+4)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// The profile hash carries Version, so this is the scoring change: the
	// scores are gone and the vectors are not.
	p := synthProfile(t)
	p.Target += " with a new scoring version"
	m, rep := warmIn(t, newSpy(), p, dir, docs)
	if rep.Rescored != 6 || rep.Scored != 0 || rep.VectorsRebuilt || rep.VectorsAppended != 0 {
		t.Fatalf("the log was not used: %+v", rep)
	}
	assertSame(t, matchesOf(t, m, docs), oracle(t, p, docs))
	after, err := VectorStatus(dir)
	if err != nil || after.Generation != before.Generation || after.Records != 6 || after.Version != ChunkVersion {
		t.Fatalf("the log was replaced: before %+v after %+v %v", before, after, err)
	}
}

func TestVectorsTornTailTruncated(t *testing.T) {
	docs := vecDocs(6)
	cases := map[string]struct {
		damage   func(t *testing.T, dir string)
		rescored int
		torn     func(int64) bool
	}{
		"partial record": {func(t *testing.T, dir string) {
			st, _ := os.Stat(logPath(dir))
			if err := os.Truncate(logPath(dir), st.Size()-10); err != nil {
				t.Fatal(err)
			}
		}, 5, func(n int64) bool { return n > 4 }},
		"partial length prefix": {func(t *testing.T, dir string) {
			f, err := os.OpenFile(logPath(dir), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			f.Write([]byte{9, 9})
		}, 6, func(n int64) bool { return n == 2 }},
		"whole last record with a bad checksum": {func(t *testing.T, dir string) {
			hdr, recs := readLog(t, dir)
			recs[len(recs)-1][len(recs[len(recs)-1])-1] ^= 0xff
			writeLog(t, dir, hdr, recs...)
		}, 5, func(n int64) bool { return n > 4 }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			warmIn(t, newSpy(), synthProfile(t), dir, docs)
			c.damage(t, dir)
			m, rep := warmIn(t, newSpy(), profile2(t), dir, docs)
			if !c.torn(rep.VectorsTornBytes) || rep.VectorsRebuilt || rep.VectorsErr == "" {
				t.Errorf("torn tail not reported: %+v", rep)
			}
			if rep.Rescored != c.rescored || rep.Scored != 6-c.rescored {
				t.Errorf("rescored %d, scored %d: %+v", rep.Rescored, rep.Scored, rep)
			}
			covered(t, rep, 6)
			assertSame(t, matchesOf(t, m, docs), oracle(t, profile2(t), docs))
			st, err := VectorStatus(dir)
			if err != nil || st.TornBytes != 0 || st.Records != 6 {
				t.Errorf("log not whole afterwards: %+v %v", st, err)
			}
			// And it appends cleanly afterwards.
			_, rep = warmIn(t, newSpy(), profile3(t), dir, docs)
			if rep.Rescored != 6 || rep.VectorsCorrupt != 0 || rep.VectorsErr != "" {
				t.Errorf("after repair: %+v", rep)
			}
		})
	}
	t.Run("torn header", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "cache")
		warmIn(t, newSpy(), synthProfile(t), dir, docs)
		raw, _ := os.ReadFile(logPath(dir))
		if err := os.WriteFile(logPath(dir), raw[:len(vecMagic)+6], 0o600); err != nil {
			t.Fatal(err)
		}
		m, rep := warmIn(t, newSpy(), profile2(t), dir, docs)
		if rep.Scored != 6 || rep.Rescored != 0 || rep.VectorsRebuilt {
			t.Errorf("%+v", rep)
		}
		assertSame(t, matchesOf(t, m, docs), oracle(t, profile2(t), docs))
		if st, err := VectorStatus(dir); err != nil || st.Records != 6 {
			t.Errorf("%+v %v", st, err)
		}
	})
}

func TestVectorsCorruptMidFileFailsClosed(t *testing.T) {
	docs := vecDocs(8)
	p2 := profile2(t)
	want := oracle(t, p2, docs)
	type tc struct {
		damage    func(t *testing.T, dir string)
		rebuilt   bool // the whole log is removed and rebuilt
		corrupt   int  // records read and refused
		stale     int
		rescored  int
		healsInto bool // a later profile change rescores everything
	}
	patch := func(t *testing.T, dir string, off int64, b ...byte) {
		t.Helper()
		f, err := os.OpenFile(logPath(dir), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteAt(b, off); err != nil {
			t.Fatal(err)
		}
	}
	recOff := func(t *testing.T, dir string, i int) int64 {
		hdr, recs := readLog(t, dir)
		off := int64(len(hdr))
		for _, r := range recs[:i] {
			off += int64(len(r))
		}
		return off
	}
	splice := func(t *testing.T, dir string, i int, rec []byte) {
		hdr, recs := readLog(t, dir)
		recs[i] = rec
		writeLog(t, dir, hdr, recs...)
	}
	cases := map[string]tc{
		"flipped length byte": {damage: func(t *testing.T, dir string) {
			patch(t, dir, recOff(t, dir, 3)+3, 0x7f) // length now far over any record
		}, rebuilt: true},
		"length over the maximum": {damage: func(t *testing.T, dir string) {
			patch(t, dir, recOff(t, dir, 1), binary.LittleEndian.AppendUint32(nil, maxRecLen+1)...)
		}, rebuilt: true},
		"length that disagrees with the shape": {damage: func(t *testing.T, dir string) {
			_, recs := readLog(t, dir)
			ln := binary.LittleEndian.Uint32(recs[2])
			patch(t, dir, recOff(t, dir, 2), binary.LittleEndian.AppendUint32(nil, ln-4)...)
		}, rebuilt: true},
		"flipped payload byte": {damage: func(t *testing.T, dir string) {
			patch(t, dir, recOff(t, dir, 3)+4+recFixed+5, 0xA5)
		}, corrupt: 1, rescored: 7, healsInto: true},
		"wrong width field": {damage: func(t *testing.T, dir string) {
			patch(t, dir, recOff(t, dir, 2)+4+18, 8, 0)
		}, rebuilt: true},
		"a record of another width": {damage: func(t *testing.T, dir string) {
			splice(t, dir, 2, craftRecord(hashOf(docs[2]), nChunks(docs[2]), 8, oneHot))
		}, rebuilt: true},
		"every record of another width": {damage: func(t *testing.T, dir string) {
			hdr, _ := readLog(t, dir)
			var recs [][]byte
			for _, d := range docs {
				recs = append(recs, craftRecord(hashOf(d), nChunks(d), 8, oneHot))
			}
			writeLog(t, dir, hdr, recs...)
		}, rebuilt: true},
		"chunk count differs from the chunking": {damage: func(t *testing.T, dir string) {
			splice(t, dir, 1, craftRecord(hashOf(docs[1]), nChunks(docs[1])-1, 256, oneHot))
		}, stale: 1, rescored: 7, healsInto: true},
		"more chunks than a posting can have": {damage: func(t *testing.T, dir string) {
			splice(t, dir, 4, craftRecord(hashOf(docs[4]), MaxPostingChunks+1, 256, oneHot))
		}, rebuilt: true},
		"zero chunks": {damage: func(t *testing.T, dir string) {
			splice(t, dir, 4, craftRecord(hashOf(docs[4]), 0, 256, oneHot))
		}, rebuilt: true},
		"a zero vector": {damage: func(t *testing.T, dir string) {
			splice(t, dir, 5, craftRecord(hashOf(docs[5]), nChunks(docs[5]), 256, func(int, int) float32 { return 0 }))
		}, corrupt: 1, rescored: 7, healsInto: true},
		"a non-finite value": {damage: func(t *testing.T, dir string) {
			splice(t, dir, 5, craftRecord(hashOf(docs[5]), nChunks(docs[5]), 256, func(i, k int) float32 {
				if k == 3 {
					return float32(math.NaN())
				}
				return 0.1
			}))
		}, corrupt: 1, rescored: 7, healsInto: true},
		"bad magic": {damage: func(t *testing.T, dir string) { patch(t, dir, 0, 'X') }, rebuilt: true},
		"unknown format": {damage: func(t *testing.T, dir string) {
			patch(t, dir, int64(len(vecMagic)), 9, 0, 0, 0)
		}}, // not this software's layout: refused, not guessed at, not removed
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			warmIn(t, newSpy(), synthProfile(t), dir, docs)
			c.damage(t, dir)
			damaged, _ := os.ReadFile(logPath(dir))
			m, err := NewMatcher(newSpy(), p2, dir)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := m.Warm(context.Background(), docs, 1000)
			if err != nil {
				t.Fatalf("Warm: %v %+v", err, rep)
			}
			covered(t, rep, len(docs))
			// A chunk-count difference is counted (VectorsStale) but is no fault in
			// the log; every other damage must be reported as one.
			if (rep.VectorsErr == "" || rep.Err == "") && c.stale == 0 {
				t.Errorf("a damaged log looked like a good one: %+v", rep)
			}
			if rep.VectorsRebuilt != c.rebuilt || rep.VectorsCorrupt != c.corrupt || rep.VectorsStale != c.stale {
				t.Errorf("rebuilt=%v corrupt=%d stale=%d, want %v %d %d: %+v", rep.VectorsRebuilt, rep.VectorsCorrupt, rep.VectorsStale, c.rebuilt, c.corrupt, c.stale, rep)
			}
			if name == "unknown format" {
				// Unusable and kept as it was; every posting embedded.
				if rep.Scored != 8 || rep.Rescored != 0 || rep.VectorsAppended != 0 {
					t.Errorf("%+v", rep)
				}
				if now, _ := os.ReadFile(logPath(dir)); string(now) != string(damaged) {
					t.Error("a file of an unknown format was modified")
				}
			} else if rep.Rescored != c.rescored || rep.Scored != 8-c.rescored {
				t.Errorf("rescored %d scored %d: %+v", rep.Rescored, rep.Scored, rep)
			}
			assertSame(t, matchesOf(t, m, docs), want) // never scored from bad data
			if c.healsInto {
				_, rep = warmIn(t, newSpy(), profile3(t), dir, docs)
				if rep.Rescored != 8 || rep.VectorsCorrupt != 0 || rep.VectorsStale != 0 {
					t.Errorf("did not heal: %+v", rep)
				}
			}
		})
	}
}

func TestVectorsDuplicateHashLastWins(t *testing.T) {
	docs := vecDocs(6)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	x := docs[2]
	_, recs := readLog(t, dir)
	genuine := append([]byte(nil), recs[recIndex(t, recs, x)]...)
	f, err := os.OpenFile(logPath(dir), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	appendRec := func(b []byte) {
		if _, err := f.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	// A later record for the same text (a crash after a re-embed, or another
	// writer) shadows the earlier one: here an unrelated vector set.
	appendRec(craftRecord(hashOf(x), nChunks(x), 256, func(i, k int) float32 {
		if k == 7 {
			return 1
		}
		return 0
	}))
	p2 := profile2(t)
	want := oracle(t, p2, docs)
	m, rep := warmIn(t, newSpy(), p2, dir, docs)
	if rep.Rescored != 6 || rep.VectorsCorrupt != 0 {
		t.Fatalf("%+v", rep)
	}
	got := matchesOf(t, m, docs)
	if reflect.DeepEqual(got[x.Key], want[x.Key]) {
		t.Fatal("the earlier record was used: the last one must win")
	}
	for _, d := range docs {
		if d.Key != x.Key && !reflect.DeepEqual(got[d.Key], want[d.Key]) {
			t.Errorf("%s changed", d.Key)
		}
	}
	// Restore the genuine vectors as the newest record: back to exact.
	appendRec(genuine)
	f.Close()
	p3 := profile3(t)
	m3, rep := warmIn(t, newSpy(), p3, dir, docs)
	if rep.Rescored != 6 {
		t.Fatalf("%+v", rep)
	}
	assertSame(t, matchesOf(t, m3, docs), oracle(t, p3, docs))
}

// ---- the bound ----

func TestVectorsBoundNeverExceeded(t *testing.T) {
	docs := vecDocs(30)
	dir := filepath.Join(t.TempDir(), "cache")
	const bound = 24 << 10
	m, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	m.vecMax = bound
	keep := map[string]float64{
		TextHash(docs[0].Title, docs[0].Description): 0,
		TextHash(docs[1].Title, docs[1].Description): 0,
	}
	m.SetVectorPriority(keep)
	evicted, refused := 0, 0
	for _, d := range docs {
		rep, err := m.Warm(context.Background(), []Doc{d}, 10)
		if err != nil {
			t.Fatalf("%s: %v %+v", d.Key, err, rep)
		}
		st, err := os.Stat(logPath(dir))
		if err != nil || st.Size() > bound {
			t.Fatalf("after %s the log is %v bytes (%v), bound %d", d.Key, st.Size(), err, bound)
		}
		evicted += rep.VectorsEvicted
		refused += rep.VectorsRefused
	}
	if evicted == 0 || refused != 0 {
		t.Errorf("evicted %d refused %d: the bound never bit, or vectors were refused though eviction was possible", evicted, refused)
	}
	if m.Len() != 30 {
		t.Errorf("%d scores saved, want 30: eviction must not cost scores", m.Len())
	}
	for _, d := range docs[:2] {
		if _, ok := m.vs.idx[hashOf(d)]; !ok {
			t.Errorf("%s is in the live set and was evicted", d.Key)
		}
	}
	st, err := VectorStatus(dir)
	if err != nil || st.Records == 0 || st.Records >= 30 || st.Bytes > bound {
		t.Errorf("%+v %v", st, err)
	}
	// Without a live set only age decides: the newest survive.
	m2, _ := newMatcher(t, newSpy())
	m2.vecMax = bound
	for _, d := range docs {
		mustWarm(t, m2, d)
	}
	if _, ok := m2.vs.idx[hashOf(docs[29])]; !ok {
		t.Error("the newest posting's vectors were evicted before older ones")
	}
	if _, ok := m2.vs.idx[hashOf(docs[0])]; ok {
		t.Error("the oldest posting's vectors survived a log that cannot hold them all")
	}
}

func TestVectorsRefusedWhenNothingFits(t *testing.T) {
	docs := vecDocs(3)
	m, dir := newMatcher(t, newSpy())
	m.vecMax = int64(vecHdrFixed+len(m.model)) + 64 // smaller than one record
	rep, err := m.Warm(context.Background(), docs, 10)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	if rep.Scored != 3 || m.Len() != 3 {
		t.Errorf("scores must still be saved: %+v len=%d", rep, m.Len())
	}
	if rep.VectorsAppended != 0 || rep.VectorsRefused != 3 || rep.VectorsErr == "" || rep.Err == "" {
		t.Errorf("refusal not reported: %+v", rep)
	}
	if !strings.Contains(rep.VectorsErr, "size bound") {
		t.Errorf("error %q does not say why", rep.VectorsErr)
	}
	if st, err := os.Stat(logPath(dir)); err != nil || st.Size() > m.vecMax {
		t.Errorf("log %v (%v) passes the bound %d", st.Size(), err, m.vecMax)
	}
}

func TestVectorsCompactDropsSupersededAndKeepsData(t *testing.T) {
	dir := t.TempDir()
	s := newChunkStore(logPath(dir), "model-x", MaxVectorBytes)
	s.compactMin = 1
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	vec := func(n int, seed float32) [][]float32 {
		out := make([][]float32, n)
		for i := range out {
			v := make([]float32, 16)
			v[(i+int(seed))%16] = 1
			out[i] = v
		}
		return out
	}
	var a, b [16]byte
	a[0], b[0] = 1, 2
	if n, _, err := s.append([]vecRec{{a, vec(6, 0)}, {b, vec(3, 1)}}); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	// a is re-embedded under a different chunking: the old record is superseded.
	if n, _, err := s.append([]vecRec{{a, vec(2, 5)}}); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	before := s.end
	if !s.needsCompaction() {
		t.Fatal("dead bytes outweigh live ones and the log does not want compacting")
	}
	gen := s.gen
	ev, err := s.compact(vecPrio{}, evictTo(MaxVectorBytes))
	if err != nil || !ev.Compacted || ev.Evicted != 0 || s.end >= before || s.gen == gen {
		t.Fatalf("%+v %v end %d->%d", ev, err, before, s.end)
	}
	got, err := s.read(a, 2, 16)
	if err != nil || !reflect.DeepEqual(got, vec(2, 5)) {
		t.Fatalf("a after compaction: %v %v", got, err)
	}
	if got, err = s.read(b, 3, 16); err != nil || !reflect.DeepEqual(got, vec(3, 1)) {
		t.Fatalf("b after compaction: %v %v", got, err)
	}
	if err := s.lookup(a, 6); !errors.Is(err, errVecStale) {
		t.Errorf("old chunk count: %v", err)
	}
	if st, err := VectorStatus(dir); err != nil || st.Records != 2 || st.TornBytes != 0 || st.Bytes != s.end {
		t.Errorf("%+v %v", st, err)
	}
	s.close()
}

func TestVectorsRoundTripBitExactAndNotRenormalised(t *testing.T) {
	dir := t.TempDir()
	s := newChunkStore(logPath(dir), "model-x", MaxVectorBytes)
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	// A real unit vector (validRow's output) and one that is deliberately not
	// unit length: a reader that renormalises would change both.
	unit, ok := validRow([]float32{0.3, -0.7, 0.123456789, 1e-7, 0.5, 0.25, -0.11, 0.9}, 8)
	if !ok {
		t.Fatal("fixture")
	}
	odd := []float32{3, 4, 0, 0, 0, 0, 0, 0}
	var h [16]byte
	h[3] = 9
	if n, _, err := s.append([]vecRec{{h, [][]float32{unit, odd}}}); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	// Reopen from the file, as the next Warm would.
	s2 := newChunkStore(logPath(dir), "model-x", MaxVectorBytes)
	if _, err := s2.refresh(); err != nil {
		t.Fatal(err)
	}
	defer s2.close()
	got, err := s2.read(h, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	for k, x := range unit {
		if math.Float32bits(got[0][k]) != math.Float32bits(x) {
			t.Fatalf("component %d changed: %v -> %v", k, x, got[0][k])
		}
	}
	if !reflect.DeepEqual(got[1], odd) {
		t.Errorf("a non-unit vector was renormalised: %v", got[1])
	}
}

func TestVectorsAreOpenedOnlyWhenNeeded(t *testing.T) {
	docs := vecDocs(3)
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, docs...)
	if err := os.Remove(logPath(dir)); err != nil {
		t.Fatal(err)
	}
	rep := mustWarm(t, m, docs...)
	if rep.Cached != 3 {
		t.Fatalf("%+v", rep)
	}
	if _, err := os.Stat(logPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an all-cached warm touched the vector log: %v", err)
	}
	// A cache-only matcher never creates it either.
	fresh := filepath.Join(t.TempDir(), "none")
	lk, err := OpenLookup(synthProfile(t), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lk.Lookup("x", "y"); ok {
		t.Fatal("found")
	}
	if _, err := os.Stat(logPath(fresh)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a lookup-only matcher touched the vector log: %v", err)
	}
	if st, err := VectorStatus(fresh); err != nil || st.Present {
		t.Errorf("%+v %v", st, err)
	}
}

func TestVectorsLinkedLeafDisablesLogNotScoring(t *testing.T) {
	docs := vecDocs(3)
	m, dir := newMatcher(t, newSpy())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, logPath(dir)); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	rep, err := m.Warm(context.Background(), docs, 10)
	if err != nil || rep.Scored != 3 || rep.VectorsErr == "" || rep.Err == "" || rep.VectorsRebuilt {
		t.Fatalf("%+v %v", rep, err)
	}
	if st, _ := os.Stat(target); st.Size() != 0 {
		t.Error("the log was written through a symlink")
	}
}

// ---- concurrency and generations ----

func TestVectorsCompactionGenerationSeen(t *testing.T) {
	docs := vecDocs(10)
	a, dir := newMatcher(t, newSpy())
	mustWarm(t, a, docs...)
	before, err := VectorStatus(dir)
	if err != nil || before.Records != 10 {
		t.Fatalf("%+v %v", before, err)
	}
	if a.vs == nil || len(a.vs.idx) != 10 {
		t.Fatal("matcher a should hold an index")
	}
	oldOffsets := map[[16]byte]int64{}
	for h, loc := range a.vs.idx {
		oldOffsets[h] = loc.off
	}

	// Another process compacts: the first five postings leave and every
	// survivor moves.
	b, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]float64{}
	for _, d := range docs[5:] {
		live[TextHash(d.Title, d.Description)] = 0
	}
	b.SetVectorPriority(live)
	st, err := b.CompactVectors(context.Background())
	if err != nil || st.Evicted != 5 || st.Records != 5 || st.Generation == before.Generation {
		t.Fatalf("%+v %v", st, err)
	}

	// The other process then keeps appending until the file is longer than a's
	// index says: a scan "from the last offset" of the new file would land inside
	// a record, so only the generation tells a from an append-only growth.
	more := vecDocs(20)[10:]
	if rep, err := b.Warm(context.Background(), more, 100); err != nil || rep.Scored != 10 {
		t.Fatalf("%v %+v", err, rep)
	}
	if fi, err := os.Stat(logPath(dir)); err != nil || fi.Size() < a.vs.end {
		t.Fatalf("fixture: the log (%v bytes, %v) must outgrow a's index end %d", fi.Size(), err, a.vs.end)
	}
	docs = append(docs, more...)

	// a's scores vanish (another profile wrote them over); a's index is stale.
	if err := os.Remove(filepath.Join(dir, scoresFileName)); err != nil {
		t.Fatal(err)
	}
	rep, err := a.Warm(context.Background(), docs, 100)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	if rep.Rescored != 15 || rep.Scored != 5 || rep.VectorsCorrupt != 0 || rep.VectorsStale != 0 || rep.VectorsRebuilt {
		t.Fatalf("stale offsets were trusted or the compaction was missed: %+v", rep)
	}
	if a.vs.gen != st.Generation && a.vs.gen == before.Generation {
		t.Error("generation not refreshed")
	}
	moved := false
	for h, loc := range a.vs.idx {
		if o, ok := oldOffsets[h]; ok && o != loc.off {
			moved = true
		}
	}
	if !moved {
		t.Error("fixture: compaction moved nothing, so the test proves nothing")
	}
	assertSame(t, matchesOf(t, a, docs), oracle(t, synthProfile(t), docs))
}

func TestVectorsConcurrentWarmsBothVisible(t *testing.T) {
	_, dir := newMatcher(t, newSpy())
	a, err := NewMatcher(NewFakeEmbedder(256, synonyms), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewMatcher(NewFakeEmbedder(256, synonyms), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	all := vecDocs(40)
	da, db := all[:20], all[20:]
	var wg sync.WaitGroup
	reps := make([]Report, 2)
	errs := make([]error, 2)
	for i, w := range []struct {
		m    *Matcher
		docs []Doc
	}{{a, da}, {b, db}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reps[i], errs[i] = w.m.Warm(context.Background(), w.docs, 1000)
		}()
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || reps[i].Scored != 20 || reps[i].VectorsAppended != 20 || reps[i].VectorsErr != "" {
			t.Fatalf("warm %d: %+v %v", i, reps[i], errs[i])
		}
	}
	st, err := VectorStatus(dir)
	if err != nil || st.Records != 40 || st.TornBytes != 0 {
		t.Fatalf("one writer's vectors were lost: %+v %v", st, err)
	}
	// Both writers' postings rescore under a new profile.
	p2 := profile2(t)
	m, rep := warmIn(t, newSpy(), p2, dir, all)
	if rep.Rescored != 40 || rep.Scored != 0 || rep.VectorsCorrupt != 0 {
		t.Fatalf("%+v", rep)
	}
	assertSame(t, matchesOf(t, m, all), oracle(t, p2, all))
}

func TestCancelMidRescoreSavesPartial(t *testing.T) {
	docs := vecDocs(40)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	m, err := NewMatcher(newSpy(), profile2(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.rescoreTick = func(n int) {
		if n == 10 {
			cancel()
		}
	}
	rep, err := m.Warm(ctx, docs, 100)
	if !errors.Is(err, context.Canceled) || !rep.Canceled {
		t.Fatalf("%+v %v", rep, err)
	}
	if rep.Rescored != 10 || rep.Skipped != 30 || rep.Failed != 0 || rep.Scored != 0 {
		t.Fatalf("partial result wrong: %+v", rep)
	}
	covered(t, rep, 40)
	m2, err := NewMatcher(newSpy(), profile2(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Len() != 10 {
		t.Errorf("saved %d, rescored 10", m2.Len())
	}
	// A later warm finishes the rest from the same vectors.
	rep, err = m2.Warm(context.Background(), docs, 100)
	if err != nil || rep.Cached != 10 || rep.Rescored != 30 || rep.Scored != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestVectorStatus(t *testing.T) {
	docs := vecDocs(7)
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, docs...)
	st, err := VectorStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(logPath(dir))
	if !st.Present || st.Records != 7 || st.Bytes != fi.Size() || st.Model != m.Model() || st.Version != ChunkVersion ||
		st.Generation == 0 || st.Dim != 256 || st.TornBytes != 0 || st.LiveBytes <= 0 || st.LiveBytes >= st.Bytes {
		t.Errorf("%+v (file %d bytes)", st, fi.Size())
	}
	if err := os.WriteFile(logPath(dir), []byte("not a log at all, but long enough to hold a header"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VectorStatus(dir); !errors.Is(err, ErrCache) {
		t.Errorf("garbage: %v", err)
	}
}
