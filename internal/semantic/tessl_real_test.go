//go:build tessl && darwin && arm64

package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// realTessl opens the snapshot named by VANI_TESSL_SNAPSHOT, or skips. The
// texts below are synthetic; no personal data is embedded.
func realTessl(t *testing.T) *Tessl {
	t.Helper()
	snap := os.Getenv("VANI_TESSL_SNAPSHOT")
	if snap == "" {
		t.Skip("set VANI_TESSL_SNAPSHOT to a google/embeddinggemma-2 snapshot directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	e, err := OpenTessl(ctx, TesslConfig{Snapshot: snap})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestTesslRealSynonyms(t *testing.T) {
	e := realTessl(t)
	ctx := context.Background()
	docs, err := e.Embed(ctx, []string{"Operated Kubernetes clusters and wrote Helm charts"}, KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := e.Embed(ctx, []string{
		"Experience running k8s in production",
		"Container orchestration at scale",
		"Laminating croissant dough and plating desserts",
	}, KindQuery)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := validRow(docs[0], 0)
	var c [3]float64
	for i, q := range qs {
		v, ok := validRow(q, len(d))
		if !ok {
			t.Fatalf("row %d unusable", i)
		}
		c[i] = dot(v, d)
	}
	t.Logf("model=%s k8s=%.3f orchestration=%.3f pastry=%.3f", e.Model(), c[0], c[1], c[2])
	if !(c[0] > c[2] && c[1] > c[2]) {
		t.Fatalf("synonyms did not beat an unrelated line: %v", c)
	}
}

// TestTesslRealBankOrders scores synthetic postings against the owner's real
// bank (SEMANTIC_REAL_BANK=1, KBSYNC_BANK=<bank.json>). It prints counts and
// scores only, never bank content.
func TestTesslRealBankOrders(t *testing.T) {
	if os.Getenv("SEMANTIC_REAL_BANK") != "1" {
		t.Skip("set SEMANTIC_REAL_BANK=1 and KBSYNC_BANK to run against the real bank")
	}
	e := realTessl(t)
	b, err := kbsync.Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, "Early-career software, ML or systems engineer")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcher(e, p, filepath.Join(t.TempDir(), "semantic"))
	if err != nil {
		t.Fatal(err)
	}
	docs := []Doc{
		{Key: "ml", Title: "Machine Learning Engineer", Description: "Train and deploy deep learning models. Requirements: Python, PyTorch, experience shipping models to production, GPU training."},
		{Key: "swe", Title: "Software Engineer, Backend", Description: "Design APIs and distributed services. Requirements: a systems language, databases, cloud infrastructure, testing."},
		{Key: "data", Title: "Data Analyst", Description: "Build dashboards and reports. Requirements: SQL, spreadsheets, stakeholder communication."},
		{Key: "nurse", Title: "Registered Nurse", Description: "Provide patient care on a medical-surgical unit. Requirements: RN license, BLS certification, two years of acute care."},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	rep, err := m.Warm(ctx, docs, len(docs))
	if err != nil {
		t.Fatalf("warm: %v (%+v)", err, rep)
	}
	t.Logf("profile chunks=%d truncated=%v warm=%v report=%+v", len(p.Chunks), p.Truncated, time.Since(start).Round(time.Millisecond), rep)
	score := map[string]float64{}
	for _, d := range docs {
		mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
		if !ok {
			t.Fatalf("%s not scored", d.Key)
		}
		score[d.Key] = mt.Score
		t.Logf("%s score=%.3f raw=%.3f", d.Key, mt.Score, mt.Raw)
	}
	if !(min(score["ml"], score["swe"]) > score["data"] && score["data"] > score["nurse"]) {
		t.Fatalf("real bank does not order engineering > analyst > nursing: %v", score)
	}
}

func TestTesslRealSimilarityKind(t *testing.T) {
	e := realTessl(t)
	rows, err := e.Embed(context.Background(), []string{"LinkedIn", "LinkedIn profile URL", "Years of experience"}, KindSimilarity)
	if err != nil {
		t.Fatalf("STS embed: %v", err)
	}
	var v [3][]float32
	for i, r := range rows {
		var ok bool
		if v[i], ok = validRow(r, 0); !ok {
			t.Fatalf("row %d unusable", i)
		}
	}
	same, other := dot(v[0], v[1]), dot(v[0], v[2])
	t.Logf("sts: linkedin~linkedin-url=%.3f linkedin~experience=%.3f", same, other)
	if same <= other {
		t.Fatalf("near-duplicate labels did not beat an unrelated one: %.3f <= %.3f", same, other)
	}
	if _, err := e.Embed(context.Background(), []string{"x"}, Kind("nope")); !errors.Is(err, ErrEmbed) {
		t.Fatalf("unknown kind: %v, want ErrEmbed", err)
	}
}

func TestTesslRealMatcherOrders(t *testing.T) {
	e := realTessl(t)
	raw := []byte(`{"entries":{
	  "e1":{"title":"Machine Learning Engineer","org":"SyntheticCo","dates":"2024 - Present","location":"Nowhere","stack":"Python, PyTorch, Kubernetes, CUDA","bullets":["b1","b2"]},
	  "e2":{"title":"Software Engineer Intern","org":"OtherCo","dates":"2023","location":"Elsewhere","stack":"Go, PostgreSQL, gRPC","bullets":["b3"]}},
	 "bullets":{
	  "b1":{"text":"Trained transformer models on multi-GPU clusters and cut inference latency"},
	  "b2":{"text":"Built feature pipelines feeding a real-time recommendation service"},
	  "b3":{"text":"Wrote backend services with gRPC APIs and SQL migrations"}}}`)
	b, err := kbsync.Parse(raw)
	if err != nil {
		t.Fatalf("synthetic bank does not parse: %v", err)
	}
	p, err := ProfileFromBank(b, "Early-career machine learning or backend software engineer")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcher(e, p, filepath.Join(t.TempDir(), "semantic"))
	if err != nil {
		t.Fatal(err)
	}
	docs := []Doc{
		{Key: "on", Title: "ML Engineer, Inference", Description: "You will optimize deep learning model serving on GPUs. Requirements: experience with PyTorch or a similar framework, container orchestration (k8s), and profiling CUDA kernels."},
		{Key: "mid", Title: "Backend Engineer", Description: "Build distributed services in Go. Requirements: RPC frameworks, relational databases, and on-call experience."},
		{Key: "off", Title: "Pastry Chef", Description: "Prepare laminated doughs, plated desserts and seasonal menus. Requirements: culinary school and three years in a bakery."},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep, err := m.Warm(ctx, docs, len(docs))
	if err != nil {
		if errors.Is(err, ErrCalibration) {
			t.Fatalf("calibration degenerate on the real model: %v", err)
		}
		t.Fatalf("warm: %v (%+v)", err, rep)
	}
	score := map[string]float64{}
	for _, d := range docs {
		mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
		if !ok {
			t.Fatalf("%s: no cached match after warm (%+v)", d.Key, rep)
		}
		score[d.Key] = mt.Score
		t.Logf("%s score=%.3f raw=%.3f evidence=%v", d.Key, mt.Score, mt.Raw, mt.Evidence)
	}
	if !(score["on"] > score["mid"] && score["mid"] > score["off"]) {
		t.Fatalf("order on > mid > off broken: %v", score)
	}
}
