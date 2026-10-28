package jobview

import (
	"fmt"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Two display-only notes about a posting's company history, computed by
// internal/samecompany and carried on a Job (and, for the first, on `jobs
// find`'s reply). They only annotate (Rule 7): nothing is dropped or blocked,
// the person decides. Each carries its own display Text, so no surface words it
// again.

// noteTitleRunes bounds a title quoted inside a note's text.
const noteTitleRunes = 120

// RepeatApplyNote warns that the owner has already applied to other postings at
// this posting's company inside the window some application systems reject a
// repeat application in.
type RepeatApplyNote struct {
	// Count is how many other postings at the company were applied to in the
	// window; Latest* is the most recent of them.
	Count       int         `json:"count"`
	WindowDays  int         `json:"window_days"`
	LatestKey   posting.Key `json:"latest_key"`
	LatestTitle string      `json:"latest_title"`
	LatestAt    time.Time   `json:"latest_at"`
	Text        string      `json:"text"`
}

// NewRepeatApplyNote words the warning for company. A title is untrusted
// posting text and is cleaned and bounded.
func NewRepeatApplyNote(company string, count, windowDays int, latestKey posting.Key, latestTitle string, latestAt time.Time) *RepeatApplyNote {
	title := CleanText(latestTitle, noteTitleRunes, false)
	if title == "" {
		title = "(untitled)"
	}
	where := CleanText(company, FieldRunes, false)
	if where == "" {
		where = "this company"
	}
	roles := "roles"
	if count == 1 {
		roles = "role"
	}
	return &RepeatApplyNote{
		Count: count, WindowDays: windowDays, LatestKey: latestKey, LatestTitle: title, LatestAt: latestAt,
		Text: fmt.Sprintf("You applied to %d other %s at %s in the last %d days (most recent: %s, %s). Some application systems silently reject a repeat application to one company.",
			count, roles, where, windowDays, title, latestAt.UTC().Format(time.DateOnly)),
	}
}

// RepostNote says a posting is likely the same role as an earlier posting at
// the company, relisted under a new key.
type RepostNote struct {
	OfKey     posting.Key `json:"of_key"`
	Title     string      `json:"title"`
	FirstSeen time.Time   `json:"first_seen"`
	Text      string      `json:"text"`
}

// NewRepostNote words the note for an earlier posting first seen at firstSeen.
func NewRepostNote(ofKey posting.Key, title string, firstSeen time.Time) *RepostNote {
	t := CleanText(title, noteTitleRunes, false)
	if t == "" {
		t = "(untitled)"
	}
	return &RepostNote{OfKey: ofKey, Title: t, FirstSeen: firstSeen,
		Text: fmt.Sprintf("likely repost of %s first seen %s", t, firstSeen.UTC().Format(time.DateOnly))}
}
