package google

import (
	"context"
	"testing"
)

// Rows that repeat a job id with a total equal to the distinct jobs: the other
// four adapters compare the total with DISTINCT rows; google compares the raw
// row count, so a repeated id fails a board whose total is correct.
func TestW3cadvRepeatedIDsWithTotalOfDistinctJobs(t *testing.T) {
	s, _ := newSource(map[string]string{searchURL("x", nil): page(3, 0, 0, 1, 1, 2)})
	got, err := s.List(context.Background(), board("x"))
	if err != nil {
		t.Fatalf("a first page of 5 rows over 3 distinct jobs under total 3 failed: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d postings, want 3 distinct", len(got))
	}
}
