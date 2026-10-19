package page

import (
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var scanFragments = []string{
	"<", ">", "/", "=", "\"", "'", " ", "\n", "-", "<!--", "-->",
	"<a href=\"/careers/1\">", "<A HREF='/Jobs/2'>", "<a HrEf=/x>", "</a>", "</A>", "</a",
	"<BASE href=\"https://e.example/\">", "<base>", "<Body class=x>", "<body>",
	"<TITLE>", "</title>", "</TiTlE>", "<script type=\"application/ld+json\">", "<SCRIPT TYPE='Application/LD+JSON'>",
	"</script>", "</SCRIPT>", "<style>", "</Style>", "<textarea>", "</TEXTAREA>",
	"{\"@type\":\"JobPosting\"}", "Senior", "Engineer", "é", "\u212a", "ſ", "</ſcript>", "<div DATA-ID=3>", "</div>",
}

func randomScanDoc(rng *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(scanFragments[rng.Intn(len(scanFragments))])
	}
	return b.String()
}

// scanHTML no longer lowers a copy of the document; every result must be
// what the old version found.
func TestScanHTMLMatchesOldScanOnRandomDocuments(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for i := range 50000 {
		doc := randomScanDoc(rng, rng.Intn(30))
		if got, want := scanHTML(doc), oldScanHTML(doc); !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d, doc %q:\n got %+v\nwant %+v", i, doc, got, want)
		}
	}
	for _, doc := range []string{benchDOM(3000), randomScanDoc(rand.New(rand.NewSource(12)), 20000)} {
		if got, want := scanHTML(doc), oldScanHTML(doc); !reflect.DeepEqual(got, want) {
			t.Fatalf("a %d-byte document scans differently (%d vs %d links)", len(doc), len(got.links), len(want.links))
		}
	}
}

// The old scan allocated two lowered copies of the document up front; what
// scanHTML allocates now is what it finds, not the document's size.
func TestScanHTMLDoesNotCopyTheDocument(t *testing.T) {
	doc := "<html><body>" + strings.Repeat("<p>no anchors here, just text</p>", 200000) + "</body></html>"
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	sc := scanHTML(doc)
	runtime.ReadMemStats(&m1)
	if sc.bodyStart == 0 {
		t.Fatal("body not found")
	}
	if alloc := m1.TotalAlloc - m0.TotalAlloc; alloc > uint64(len(doc))/4 {
		t.Errorf("scanHTML allocated %d bytes on a %d-byte document with nothing to collect", alloc, len(doc))
	}
}
