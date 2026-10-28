package semantic

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Hostile cases for cleanBulletText that TestCleanBulletTextDropsPhoneAndAuthorization
// does not already cover. Numbers are the same obvious fakes as that test.
func TestCleanBulletTextAdversarial(t *testing.T) {
	t.Run("zwj and other non-separators break the run", func(t *testing.T) {
		// 3+3+4 digits. Joined, they would be one phone and disappear.
		zwj := "dial 555\u200d123\u200d4567 now"
		if got := cleanBulletText(zwj); got != zwj {
			t.Errorf("ZWJ joined digit groups: %q", got)
		}
		// A comma is not a separator. 3+3+4 would be a phone if it were.
		comma := "cost 555,123,4567 units"
		if got := cleanBulletText(comma); got != comma {
			t.Errorf("comma joined digit groups: %q", got)
		}
		// A letter is not a separator either. The 7-digit side is still a phone.
		letter := "ext 555x1234567"
		if got := cleanBulletText(letter); got != "ext 555x" {
			t.Errorf("letter did not break the run: %q", got)
		}
	})

	t.Run("nbsp is a separator", func(t *testing.T) {
		got := cleanBulletText("Reach 555\u00a0123\u00a04567 now")
		if got != "Reach now" {
			t.Errorf("NBSP phone survived: %q", got)
		}
		// Six digits stay. NBSP is only a separator, and the plain-text pass
		// folds it to an ordinary space before the phone rule runs.
		short := cleanBulletText("code 555\u00a0456")
		if short != "code 555 456" {
			t.Errorf("six digits around NBSP: %q", short)
		}
	})

	t.Run("phone glued to a year span", func(t *testing.T) {
		// A space is a separator, so the year is not the two-group exception.
		// The whole run is one phone.
		spaced := cleanBulletText("Shipped 2019-2024 555-123-4567 at Acme")
		if spaced != "Shipped at Acme" {
			t.Errorf("space-glued year and phone: %q", spaced)
		}
		// No space: the second group is longer than a year, so nothing is kept.
		tight := cleanBulletText("Shipped 2019-2024555-123-4567 at Acme")
		if tight != "Shipped at Acme" {
			t.Errorf("tight-glued year and phone: %q", tight)
		}
		// An em dash is not a separator. The year stays; the phone does not.
		em := cleanBulletText("At Acme 2019-2024\u2014555-123-4567 shipped")
		if em != "At Acme 2019-2024\u2014 shipped" {
			t.Errorf("em-dash glue: %q", em)
		}
	})

	t.Run("ten thousand digits still return", func(t *testing.T) {
		line := "Before " + strings.Repeat("9", 10000) + " after 2019-2024"
		gotCh := make(chan string, 1)
		panicCh := make(chan any, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			gotCh <- cleanBulletText(line)
			gotCh <- cleanBulletText(strings.Repeat("9", 10000))
		}()
		var gotLine, gotOnly string
		for n := 0; n < 2; n++ {
			select {
			case r := <-panicCh:
				t.Fatalf("panic: %v", r)
			case got := <-gotCh:
				if n == 0 {
					gotLine = got
				} else {
					gotOnly = got
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleanBulletText did not return")
			}
		}
		if gotLine != "Before after 2019-2024" {
			t.Errorf("10k-digit line: %q (len %d)", truncForTest(gotLine), len(gotLine))
		}
		if gotOnly != "" {
			t.Errorf("10k digits alone: len %d", len(gotOnly))
		}
	})

	t.Run("invalid utf-8 does not panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v", r)
			}
		}()
		samples := []string{
			"\xff",
			"\xff\xfe",
			string([]byte{0x80, 0x80, 0x80}),
			"Ada Lovelace shipped \xff555-123-4567",
			"left 555\xff123-4567 right",
			"keep 2019-2024\xff",
		}
		for _, in := range samples {
			got := cleanBulletText(in)
			if strings.Contains(got, "555-123-4567") || strings.Contains(got, "123-4567") {
				t.Errorf("phone survived: in %q got %q", in, got)
			}
			if strings.Contains(in, "2019-2024") && got != "" && !strings.Contains(got, "2019-2024") {
				t.Errorf("year span lost: in %q got %q", in, got)
			}
		}
	})

	t.Run("authorization phrase split by a newline", func(t *testing.T) {
		auth := cleanBulletText("Joined Acme and am authorized\nto work in the United States")
		if auth != "Joined Acme and am in the United States" {
			t.Errorf("newline authorization: %q", auth)
		}
		sponsor := cleanBulletText("Started at Acme without\nsponsorship last year")
		if sponsor != "Started at Acme last year" {
			t.Errorf("newline sponsorship: %q", sponsor)
		}
		// The bare word is still not a match once the phrase is not one.
		bare := cleanBulletText("Reduced cloud\nsponsorship costs at Acme")
		if bare != "Reduced cloud sponsorship costs at Acme" {
			t.Errorf("bare sponsorship: %q", bare)
		}
	})

	t.Run("person name stays", func(t *testing.T) {
		got := cleanBulletText("Ada Lovelace shipped 555-123-4567")
		if got != "Ada Lovelace shipped" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		inputs := []string{
			"",
			"Ada Lovelace shipped 555-123-4567",
			"dial 555\u200d123\u200d4567 now",
			"Reach 555\u00a0123\u00a04567 now",
			"Shipped 2019-2024 555-123-4567 at Acme",
			"At Acme 2019-2024\u2014555-123-4567 shipped",
			"2019-2024",
			"2019\u20132024",
			"555 --- 4567",
			"555---4567",
			"Joined Acme and am authorized\nto work in the United States",
			"Reduced cloud sponsorship costs at Acme",
			"\xff555-123-4567",
			strings.Repeat("9", 10000),
		}
		want := make([]string, len(inputs))
		for i, in := range inputs {
			want[i] = cleanBulletText(in)
		}
		const n = 32
		errCh := make(chan string, n*len(inputs))
		var wg sync.WaitGroup
		wg.Add(n)
		for g := 0; g < n; g++ {
			go func() {
				defer wg.Done()
				for i, in := range inputs {
					if got := cleanBulletText(in); got != want[i] {
						errCh <- truncForTest(in) + " => " + truncForTest(got)
					}
				}
			}()
		}
		wg.Wait()
		close(errCh)
		for msg := range errCh {
			t.Errorf("concurrent mismatch: %s", msg)
		}
	})
}

func truncForTest(s string) string {
	if len(s) > 60 {
		return s[:60] + "...(" + strconv.Itoa(len(s)) + " bytes)"
	}
	return s
}
