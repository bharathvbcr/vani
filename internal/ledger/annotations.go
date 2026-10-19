package ledger

// Annotation keys shared by the pipeline (writer) and the digest (reader).
// One definition, so the two cannot drift apart.
const (
	// AnnRank is the digest's sort key. DevPrism reports no aggregate score,
	// so the rank is the gap coverage percentage until a better definition
	// exists; keep the two keys separate so that can change in one place.
	AnnRank = "rank"
	// AnnCoverage is DevPrism resume_gap_analysis coveragePercentage.
	AnnCoverage = "gap.coverage_pct"
	// AnnMissing lists must-have requirements DevPrism found missing or weak
	// ("a; b; c (weak)"). JD-derived, so untrusted text.
	AnnMissing = "gap.missing"
	// AnnBlocks is how many knowledge-base blocks DevPrism selected.
	AnnBlocks = "score.blocks"
	// AnnExplanation is the "why this role" note: reasons picked from the catalog
	// (internal/decide/jdreasons), each with its probability.
	AnnExplanation = "explanation"
	// AnnTriagePrefix prefixes triage slot values ("triage.<slot>"), their
	// readings ("triage.<slot>.reading"), the backend that answered each
	// ("triage.<slot>.backend") and the unsure reason ("triage.unsure").
	AnnTriagePrefix = "triage."
	// Suffixes appended to "triage.<slot>". Calibration and shadow keys exist
	// for evaluation only; no engine rule reads them.
	//
	// AnnTriageSuffixP is Jev's probability of the chosen label (4 decimals).
	AnnTriageSuffixP = ".p"
	// AnnTriageSuffixLappiScore is Lappi's top-two probability margin.
	AnnTriageSuffixLappiScore = ".lappi_score"
	// AnnTriageSuffixShadow is the shadow backend's value when it answered.
	AnnTriageSuffixShadow = ".shadow"
	// AnnTriageSuffixShadowReading and AnnTriageSuffixShadowBackend are the
	// shadow's reading (whatever it was) and its backend name, written
	// whenever a shadow was asked. They are the promotion verdict's
	// denominator: how often the shadow was asked, not only when it answered.
	AnnTriageSuffixShadowReading = ".shadow_reading"
	AnnTriageSuffixShadowBackend = ".shadow_backend"
	// AnnTriageSuffixDisagree is "true" when primary and shadow both answered
	// and chose different values.
	AnnTriageSuffixDisagree = ".disagree"
	// AnnTriageSuffixReading and AnnTriageSuffixBackend are the slot's reading
	// and the backend whose reading it is.
	AnnTriageSuffixReading = ".reading"
	AnnTriageSuffixBackend = ".backend"
	// AnnTriageUnsure is the unsure reason ("triage.unsure").
	AnnTriageUnsure = "triage.unsure"
	// AnnLCA is the sponsorship-history badge from DOL LCA data. A badge, never
	// a filter: no engine rule reads it.
	AnnLCA = "lca.badge"
	// AnnEmployer is the employer-attribute badge from public lists (IRS
	// exempt organizations, IPEDS): "likely non-profit ...". A badge, never a
	// filter and never a legal determination.
	AnnEmployer = "employer.badge"
	// AnnDescHash is the normalised description hash used for change detection.
	AnnDescHash = "desc_hash"
)
