package semantic

import "math"

// Calibration.
//
// A real embedding model's cosines sit in a narrow band (EmbeddingGemma on an
// unrelated runtime: synonyms about 0.76, unrelated about 0.55), so a raw
// score is not comparable across profiles or models. Calibrate maps it onto
// [0,1] with two points measured through the same pipeline as a posting:
//
//   - floor: the mean raw score of the off-domain anchors below, postings the
//     owner would never match (nursing, pastry, trucking...), scored against
//     the profile exactly as a real posting is. It is where "unrelated" sits
//     for this model and this profile.
//   - ceiling: the raw score of an on-domain anchor built from the target
//     summary, chunked like a posting's description (onDomainAnchor) and
//     aggregated with the same statistic (anchorDepth: the mean of its best third
//     of chunk maxima). A target of several sentences gives a sharper ceiling
//     than a one-sentence target (DefaultTarget), which is a single chunk.
//
// Why the anchor takes a top third. A posting's raw score is the mean of its
// best topChunks (8) distinct chunk maxima out of at most MaxPostingChunks (24)
// chunks, and a real posting is always at that cap: it is a top-third order
// statistic, the score of the posting's best third. An anchor that averaged ALL
// of its sentences (six, for the owner's target) was measuring a different
// statistic, a mean over the whole target including its connective sentences
// that no posting line resembles, and so sat below most fitting postings: on the
// owner's live profile and the real model the ceiling was 0.758, 82% of their
// saved jobs scored 0.95 or more, and the top of the scale carried no ordering.
// The anchor now reads the same top third, round(n*topChunks/MaxPostingChunks)
// chunks (at least one): 2 of 6, giving a ceiling of 0.807, 8.8% of saved jobs at
// 0.95 or more, 85% of them inside (0.6, 0.95), and the saved median at 0.823
// against 0.601 for the posting population. Measured 2026-10-24 (variant V5; V0
// is the old rule); the order of postings is the same under both, since the
// ceiling only rescales, so the ranking AUC is unchanged. The floor is untouched:
// moving it is a separate decision.
//
// The off-domain anchors keep reading every chunk: each is a title and three
// lines, at most four chunks, which is fewer than topChunks.
//
// The ceiling is the target-summary anchor, not the profile's own
// nearest-neighbour similarity. Self-similarity is inflated by the bank's
// near-duplicate variants (rkb entries derived from one another) and measures
// how coherent the bank is, not how a posting that fits the owner scores. The
// anchor asks the question the score answers (how close is a fitting posting
// to this profile) with the same chunking and weighting, so those effects
// cancel. A posting that fits better than the summary clamps to 1.
//
// Both points are computed once per (profile hash, model) and cached.

type anchor struct {
	title string
	desc  string
}

// onDomainAnchor is the ceiling anchor: the target summary as the description
// of a posting that has no title line. ChunkPosting splits it into sentences,
// one chunk each at weight 1 (a target names no role, so no line earns the
// title weight), and rawScoreN aggregates them as it does a posting, so a
// target of several sentences gives a ceiling that is a mean over its
// sentences. Passing the target as the title instead would make the whole
// summary one chunk cut to MaxPostingChunkBytes, a blurred paragraph that a
// posting's sharp lines out-score.
//
// A target that ChunkPosting leaves nothing of (one short phrase, a heading, a
// generic line) is a single chunk of its own text, so the ceiling always exists.
func onDomainAnchor(target string) []Chunk {
	chunks := ChunkPosting("", target)
	if len(chunks) == 0 {
		chunks = []Chunk{{Label: "line", Text: truncBytes(target, MaxPostingChunkBytes), Weight: 1}}
	}
	return chunks
}

// anchorDepth is how many of the on-domain anchor's n chunks its ceiling
// averages: the share of a full posting that rawScoreN keeps (topChunks of
// MaxPostingChunks, a third), rounded, and never fewer than one or more than a
// posting keeps. n is the chunk count as ChunkPosting returns it, before
// near-duplicate removal: a posting's third is taken of its raw chunk count too,
// and the near-duplicate rule (nearDupCos) then applies inside the top-k cut, so
// a target that restates itself fills the cut with weaker sentences and so
// cannot raise its own ceiling. (ChunkPosting already drops exact repeats.) One to four chunks
// read at depth one, five or six at two, a full 24 at topChunks. The result is
// the anchor's raw score as it is; score()'s evidence discount is for postings.
func anchorDepth(n int) int {
	k := int(math.Round(float64(n) * topChunks / MaxPostingChunks))
	return min(max(k, 1), topChunks)
}

// offDomainAnchors is the fixed floor set. Changing it changes every floor:
// bump Version.
var offDomainAnchors = []anchor{
	{"Registered Nurse", "Provide direct patient care on a medical surgical unit.\nAdminister medications and monitor vital signs for assigned patients.\nDocument assessments and coordinate discharge planning with physicians."},
	{"Pastry Chef", "Prepare breads, croissants and plated desserts for daily service.\nDecorate custom wedding cakes and manage the bakery ovens.\nOrder flour, butter and chocolate and keep the kitchen sanitary."},
	{"Long Haul Truck Driver", "Drive a tractor trailer on regional routes and deliver freight on schedule.\nInspect the vehicle before each trip and log driving hours.\nHold a valid commercial driver license with a clean record."},
	{"Retail Sales Associate", "Greet customers on the shop floor and help them find clothing.\nOperate the cash register and process returns and exchanges.\nRestock shelves and keep displays tidy during weekend shifts."},
	{"Legal Paralegal", "Draft pleadings and discovery requests for the litigation team.\nOrganize case files and prepare exhibits for depositions.\nSchedule court filings and communicate with opposing counsel."},
	{"Elementary School Teacher", "Plan daily lessons in reading and arithmetic for a third grade class.\nGrade assignments and meet with parents about student progress.\nMaintain a safe and welcoming classroom."},
	{"Dental Hygienist", "Clean teeth and take dental x-rays for patients at a family practice.\nEducate patients about brushing and gum disease.\nSterilize instruments between appointments."},
	{"Warehouse Forklift Operator", "Load and unload pallets with a forklift in a distribution center.\nScan inventory and pick orders for outbound shipment.\nFollow safety rules and keep aisles clear."},
	{"Hotel Front Desk Agent", "Welcome guests, check them in and answer questions about the hotel.\nHandle reservations, room keys and billing disputes.\nWork evening shifts and coordinate with housekeeping."},
	{"Licensed Electrician", "Install wiring, outlets and lighting in new residential buildings.\nRead blueprints and troubleshoot faulty circuits on job sites.\nComply with the electrical code and complete an apprenticeship."},
	{"Agricultural Farm Hand", "Plant and harvest crops and operate tractors in the fields.\nFeed livestock, repair fences and help with seasonal picking.\nWork outdoors in all weather."},
	{"Restaurant Server", "Take food and drink orders and serve tables in a busy dining room.\nRecommend wine, handle payments and clear dishes.\nWork nights and weekends with a friendly manner."},
	{"Physical Therapy Assistant", "Guide patients through stretching and strengthening exercises after surgery.\nRecord progress notes and set up treatment equipment.\nHelp patients regain mobility under a licensed therapist."},
	{"Insurance Claims Adjuster", "Investigate auto and property damage claims and inspect vehicles.\nNegotiate settlements with policyholders and repair shops.\nWrite estimates and approve payments within company authority."},
}
