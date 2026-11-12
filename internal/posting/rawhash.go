package posting

// RawSHA256 is the hex sha256 of s's bytes exactly as given: no whitespace
// folding or case change, unlike DescriptionHash. It is the text binding a
// reading records: the teacher store's text_sha256 (eval.HashText) and the
// triage event's triage.desc_sha256, title_sha256 and location_sha256 are all
// this hash of the raw field, so a reader can match them to a stored posting
// version byte for byte.
func RawSHA256(s string) string { return hashOf(s) }
