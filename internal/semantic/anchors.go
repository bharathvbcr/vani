package semantic

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
//     summary, chunked and aggregated exactly like a posting.
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
