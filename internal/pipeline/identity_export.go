package pipeline

import "github.com/bharathvbcr/Vani/internal/posting"

// The identity folds near-duplicate detection compares postings by, for the
// display-only readers that must agree with it on what "the same company",
// "the same role" and "the same place" mean (internal/samecompany). They are
// thin exports: the folds themselves stay with AnnotateNearDuplicates.

// LooseCompany is a company name without punctuation and legal suffixes.
func LooseCompany(s string) string { return looseCompany(s) }

// RoleTitle is a title reduced only by what never changes the job: case,
// punctuation, a work-mode tag and requisition numbers (roleTitle).
func RoleTitle(s string) string { return roleTitle(s) }

// LooseLocation is a posting's places folded the way internal/location
// compares them (looseLocation); only equality is meaningful.
func LooseLocation(p posting.Posting) string { return looseLocation(p) }
