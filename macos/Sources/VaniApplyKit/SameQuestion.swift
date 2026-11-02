import Darwin
import Foundation

/// The `sameQuestion` native op: do two job-application form labels ask for the same answer. The
/// browser extension asks it when its own matching is unsure whether a saved answer's label and a
/// page's label mean one question; the reading only annotates, the extension decides what to fill.
///
///   {"op":"sameQuestion","a":"<label>","b":"<label>"}
///     -> {"reading":"answered","value":"same"|"different"}
///      | {"reading":"abstained"|"refused"|"unavailable"|"failed","detail":"<closed code>"}
///      | {"error":"...","code":"bad_request"}        (a label missing, not a string, empty, over 300)
///
/// It asks Apple's on-device model through `vani-fm` (task `career.same_question`, README "On-device
/// decisions") and sends the two labels, nothing else: no value, no answer, no page. A label is
/// untrusted site text; it goes in the helper's prompt, never in its instructions.
///
/// Routing. The Chrome host is not sandboxed and starts `vani-fm` itself (`FMHelper`). The Safari
/// appex is sandboxed and starts nothing (Extension.entitlements: app-sandbox, no inherit; every
/// process-needing op already goes to the agent), so its `XPCAgentClient` asks the Vani Apply agent,
/// which runs the same `FMHelper`. `vani-fm` is in the app's Contents/MacOS, not in the appex.
///
/// Bounds. The whole op is under `overallSeconds` in both browsers: the helper is killed at its
/// own deadline (`directSeconds` in the Chrome host, `agentSeconds` in the agent, whose XPC hop
/// must also fit `xpcCallSeconds`), well before the helper's own 8 s watchdog. A missing helper is
/// `unavailable/no_binary` at once, with nothing started. A label `vani suggest` would not send to
/// a model is `unavailable/skipped` and does not start the helper (`SuggestSkip`).
public enum SameQuestion {
    public static let maxLabelScalars = 300
    /// Ceiling on the op as the extension sees it, helper stop and XPC hop included.
    public static let overallSeconds: TimeInterval = 6
    /// The helper's deadline when the Chrome host runs it (VaniProcess adds at most
    /// `terminationGrace` to stop it, so the op ends inside `overallSeconds`).
    public static let directSeconds: TimeInterval = 4
    /// The helper's deadline in the agent: the same, less half a second for the XPC hop.
    public static let agentSeconds: TimeInterval = 3.5
    /// How long the Safari side waits for the agent.
    public static let xpcCallSeconds: TimeInterval = overallSeconds
    /// A real reply is under 200 bytes.
    public static let maxHelperReplyBytes = 4 * 1024

    /// Fixed text, labels excluded (OnDeviceDecider puts the labels in the prompt).
    public static let instructions = "The two labels come from job-application forms. "
        + "\"same\" means one answer would correctly fill both fields; \"different\" means they ask for different things. "
        + "Judge only the wording of the labels."

    public enum Verdict: String, CaseIterable, Sendable { case same, different }

    /// The codes this op reports besides the helper's: where the helper could not be asked at all.
    /// `skipped` is suggest's pre-model skip (`SuggestSkip`): one of the labels is not sent to a model.
    public static let opDetails: Set<String> = ["no_binary", "start_failed", "timeout", "output_too_large",
                                                "helper_exit", "bad_output", "invalid_value", "agent", "unsupported", "unknown",
                                                "skipped"]

    /// Every `detail` the helper can report (OnDeviceDecider), which a reply may pass on; anything else
    /// is `unknown`, so no model or helper text reaches the extension.
    public static let helperDetails: Set<String> = [
        "refusal", "guardrail", "context_size", "unsupported_locale", "rate_limited", "timeout", "model_not_ready",
        "device_not_eligible", "apple_intelligence_not_enabled", "os_too_old", "unavailable", "decode",
        "invalid_value", "error", "declined", "bad_request_json", "bad_request_too_large", "bad_request_version",
        "bad_request_task", "bad_request_question", "bad_request_instructions", "bad_request_options",
    ]

    public static var knownDetails: Set<String> { helperDetails.union(opDetails) }

    // MARK: the request

    public struct Labels: Equatable, Sendable {
        public var a: String
        public var b: String
    }

    /// Two labels of 1 to `maxLabelScalars` characters after trimming. A label is then flattened to
    /// one line (runs of whitespace become a space, control characters go), so it cannot start a
    /// new line of the prompt. Nil when either is not a string or is empty or too long.
    public static func labels(a: Any?, b: Any?) -> Labels? {
        guard let a = label(a), let b = label(b) else { return nil }
        return Labels(a: a, b: b)
    }

    static func label(_ raw: Any?) -> String? {
        guard let s = raw as? String else { return nil }
        let trimmed = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (1...maxLabelScalars).contains(trimmed.unicodeScalars.count) else { return nil }
        var flat = String.UnicodeScalarView()
        var gap = false
        for scalar in trimmed.unicodeScalars {
            if CharacterSet.whitespacesAndNewlines.contains(scalar) || CharacterSet.controlCharacters.contains(scalar) {
                // A control character is a separator too ("a\u{7}b" stays two words), never a line break.
                gap = !flat.isEmpty
            } else {
                if gap { flat.append(" "); gap = false }
                flat.append(scalar)
            }
        }
        let out = String(flat)
        return out.isEmpty ? nil : out
    }

    /// The vani-fm request (README "On-device decisions", contract v1).
    public static func helperRequest(_ labels: Labels) -> Data {
        let question = "Label A (untrusted text copied from a job site):\n\(labels.a)\n\n"
            + "Label B (untrusted text copied from a job site):\n\(labels.b)"
        let object: [String: Any] = [
            "v": OnDeviceDecider.version, "task": OnDeviceDecider.sameQuestionTask, "question": question,
            "options": OnDeviceDecider.sameQuestionOptions, "instructions": instructions,
        ]
        // Strings and an Int only, so encoding cannot fail.
        return (try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])) ?? Data()
    }

    // MARK: the reply

    public static func reply(_ reading: OnDeviceDecider.Reading, value: String? = nil, detail: String? = nil) -> [String: Any] {
        var out: [String: Any] = ["reading": reading.rawValue]
        if let value { out["value"] = value }
        if let detail { out["detail"] = detail }
        return out
    }

    static func unavailable(_ detail: String) -> [String: Any] { reply(.unavailable, detail: detail) }

    /// The helper's stdout (one JSON line) as an op reply. Anything that is not exactly the contract
    /// is `failed/bad_output`; an answer that is not `same` or `different` is `failed/invalid_value`;
    /// a detail outside the closed set is `unknown`. The helper's text never passes through.
    public static func reply(helperOutput data: Data) -> [String: Any] {
        guard let text = String(data: data, encoding: .utf8) else { return reply(.failed, detail: "bad_output") }
        let line = text.hasSuffix("\n") ? String(text.dropLast()) : text
        guard !line.isEmpty, !line.contains(where: \.isNewline),
              let r = try? JSONDecoder().decode(OnDeviceDecider.Reply.self, from: Data(line.utf8)) else {
            return reply(.failed, detail: "bad_output")
        }
        guard r.v == OnDeviceDecider.version else { return reply(.failed, detail: "bad_output") }
        if r.reading == .answered {
            guard let value = r.value, Verdict(rawValue: value) != nil else { return reply(.failed, detail: "invalid_value") }
            return reply(.answered, value: value)
        }
        let detail = r.detail.map { knownDetails.contains($0) ? $0 : "unknown" }
        return reply(r.reading, detail: detail)
    }

    /// Re-checks a reply that crossed XPC from an agent (possibly an older build): the same
    /// closed shapes, so nothing but a reading, one of two values and a known code reaches the page.
    public static func checked(_ object: [String: Any]) -> [String: Any] {
        guard let name = object["reading"] as? String, let reading = OnDeviceDecider.Reading(rawValue: name) else {
            return reply(.failed, detail: "bad_output")
        }
        if reading == .answered {
            guard let value = object["value"] as? String, Verdict(rawValue: value) != nil else {
                return reply(.failed, detail: "invalid_value")
            }
            return reply(.answered, value: value)
        }
        let detail = (object["detail"] as? String).map { knownDetails.contains($0) ? $0 : "unknown" }
        return reply(reading, detail: detail)
    }

    /// The op's reply for a finished helper process.
    static func reply(outcome o: VaniProcess.Outcome) -> [String: Any] {
        if o.launchError != nil { return unavailable("start_failed") }
        if o.timedOut { return unavailable("timeout") }
        if o.truncated { return reply(.failed, detail: "output_too_large") }
        guard o.status == 0 else { return reply(.failed, detail: "helper_exit") }
        return reply(helperOutput: o.stdout)
    }

    /// The agent's answer to one XPC call (Safari's route): the labels checked again, since the agent
    /// trusts no caller, then the helper. Never an error string for a reading that could not be had.
    public static func agentReply(a: String, b: String, helper: SameQuestionBackend) -> (reply: Data?, error: String?) {
        guard let checked = labels(a: a, b: b) else { return (nil, "not two labels of 1 to \(maxLabelScalars) characters") }
        // Suggest's skip is before the helper. `labels` flattens format characters into spaces, which
        // would split a word folds() joins, so the original strings are checked too.
        let object = preModelSkip(rawA: a, rawB: b, labels: checked) ?? helper.compare(checked)
        guard let data = try? JSONSerialization.data(withJSONObject: object) else { return (nil, "the reply could not be encoded") }
        return (data, nil)
    }

    /// `unavailable`/`skipped` when either label is one suggest would not send to a model, else nil.
    static func preModelSkip(_ labels: Labels) -> [String: Any]? {
        preModelSkip(rawA: labels.a, rawB: labels.b, labels: labels)
    }

    /// `rawA`/`rawB` are the page labels before `labels()` turns format characters into spaces.
    /// Folds joins those characters; the flattened pair is checked as well, since that is what a
    /// direct caller of `FMHelper` holds. Either string on either side is enough.
    static func preModelSkip(rawA: String, rawB: String, labels: Labels) -> [String: Any]? {
        if SuggestSkip.applies(rawA) || SuggestSkip.applies(rawB)
            || SuggestSkip.applies(labels.a) || SuggestSkip.applies(labels.b) {
            return unavailable("skipped")
        }
        return nil
    }
}

/// Whatever answers a `sameQuestion`: `FMHelper` in the Chrome host and the agent, `XPCAgentClient`
/// for Safari. `labels` are already checked and flattened; the reply is a closed op reply.
public protocol SameQuestionBackend: Sendable {
    func compare(_ labels: SameQuestion.Labels) -> [String: Any]
}

/// Runs the `vani-fm` helper once per question, in a minimal environment, under a deadline.
public struct FMHelper: SameQuestionBackend {
    public var binary: URL
    public var seconds: TimeInterval

    public init(binary: URL, seconds: TimeInterval = SameQuestion.directSeconds) {
        self.binary = binary
        self.seconds = seconds
    }

    /// The helper bundled with this app: `helpers` is Contents/Helpers (where `vani` and this process
    /// live), the helper is in the sibling Contents/MacOS: the rule fmbackend.go applies to `vani`.
    public static func bundled(helpers: URL = VaniProcess.helpersDirectory(),
                               seconds: TimeInterval = SameQuestion.directSeconds) -> FMHelper {
        FMHelper(binary: helpers.deletingLastPathComponent().appendingPathComponent("MacOS/vani-fm"), seconds: seconds)
    }

    /// An absolute path to a regular file this user can execute (a directory is "executable" to
    /// FileManager, and must not be started).
    static func runnable(_ url: URL) -> Bool {
        var st = stat()
        return url.path.hasPrefix("/") && stat(url.path, &st) == 0 && (st.st_mode & S_IFMT) == S_IFREG
            && access(url.path, X_OK) == 0
    }

    public func compare(_ labels: SameQuestion.Labels) -> [String: Any] {
        // Suggest's skip wins over a missing binary: nothing is started either way, and the code
        // says why. A direct caller cannot launch vani-fm for a label suggest would not send.
        if let skipped = SameQuestion.preModelSkip(labels) { return skipped }
        // Absent or not executable: nothing is started and the answer is immediate.
        guard Self.runnable(binary) else { return SameQuestion.unavailable("no_binary") }
        let outcome = VaniProcess.run(binary, arguments: [], environment: ["HOME": NSHomeDirectory()],
                                      input: SameQuestion.helperRequest(labels), timeout: seconds,
                                      maxOutputBytes: SameQuestion.maxHelperReplyBytes)
        return SameQuestion.reply(outcome: outcome)
    }
}

/// Safari's route: the agent runs the helper. An agent that is not running, or predates this call,
/// answers `unavailable/agent` rather than an error, so the extension treats it like any other
/// "no reading" and falls back to its own matching.
extension XPCAgentClient: SameQuestionBackend {
    public func compare(_ labels: SameQuestion.Labels) -> [String: Any] {
        let r = call(timeout: SameQuestion.xpcCallSeconds) { agent, done, box in
            agent.sameQuestion(a: labels.a, b: labels.b) { data, error in
                if let data { box.set(["reply": data]) } else { box.set(["error": error ?? "the agent sent no result"]) }
                done.signal()
            }
        }
        guard let data = r["reply"] as? Data, let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
            return SameQuestion.unavailable("agent")
        }
        return SameQuestion.checked(object)
    }
}

// Suggest's pre-model skip for sameQuestion.
//
// Contract: `Guard(label) != "" && NeverDrafted(label) != ""`, the condition
// suggest.Engine.triage uses before it asks a model (internal/suggest/suggest.go).
// Guard is fieldtriage.Guard: guardForms(folds(label)) in
// internal/decide/fieldtriage/fieldtriage.go and fold.go (folds, fold, baseLetter,
// mixedScript). NeverDrafted is draft.NeverDrafted in internal/draft/draft.go:
// fieldtriage.Sensitive, except when the category is Consent and essayPromptRE
// matches the lowercased original label, in which case it returns "".
// broaderRE is not part of this skip. A drift from those functions is a bug.
private enum SuggestSkip {
    private static let workAuth = "work_authorization"
    private static let selfID = "self_identification"
    private static let consent = "legal_consent"

    // Exact pattern text. `\b` and `\w` are RE2's ASCII word class; NSRegularExpression's
    // are Unicode, so `re2` rewrites only those two escapes. A drift is a bug.
    private static let workAuthPattern = #"(?i)\b(visa|sponsor(ship)?|work(ing)? ?authori[sz]ation|authori[sz]ed to work|right to work|work rights|citizens?|citizenship|nationality|nationals|national of|immigration|work permit|green card|permanent residen(t|ts|ce|cy)|(eligible|eligibility|legally entitled) to work|legally (entitled|permitted|allowed|authori[sz]ed|eligible|able)|u\.? ?s\.? ?persons?|employment (authori[sz]ation|eligibility)|h-?1b|h-?4|l-?1|tn status|f[- ]?1 (visa|student|status))\b|\b(?-i:OPT|CPT)\b"#
    private static let selfIDPattern = #"(?i)\b(gender|sex|sexual|sexuality|(fe)?male|race|racial|ethnic(ity|al)?|hispanic|latin[oax]|veteran|military|armed forces|uniformed services|disability|disabilit(y|ies)|disabled|handicap(ped)?|medical (condition|history)|health (condition|status)|sexual orientation|lgbt\w*|queer|gay|lesbian|bisexual|non-?binary|cisgender|transgender|african[- ]american|caucasian|native american|alaska(n)? native|american indian|pacific islander|native hawaiian|indigenous|asian|age|religio(n|us)|pronouns?|criminal|convict(ed|ion|ions)?|marital|marital status)\b|\b(over|under|above|below|older than|younger than|at least|age) (16|18|21|40|55|62|65)\b|\b(16|18|21|40|55|62|65) ?(\+|or (older|over|above|younger|under|more)|years old|years of age)|(^|[,/(]|\bor) ?(black|white)\b ?($|[,/)]|\bor\b)"#
    private static let consentPattern = #"(?i)\b(agree|agreement|arbitrat\w*|consent|acknowledg\w*|certify|terms (and|&) conditions|privacy (notice|policy)|ai policy)\b"#
    private static let essayPattern = #"\b(describe|tell us|tell me|a time (when |you )|explain|walk us through|give an example|share an example|how did you|how do you)\b"#

    private static let latin1Upper = "AAAAAAACEEEEIIIIDNOOOOO-OUUUUYTs"
    private static let latin1Lower = "aaaaaaaceeeeiiiidnooooo-ouuuuyty"
    private static let latinExtA = "AaAaAaCcCcCcCcDdDdEeEeEeEeEeGgGgGgGgHhHhIiIiIiIiIiIiJjKkkLlLlLlLlLlNnNnNnnNnOoOoOoOoRrRrRrSsSsSsSsTtTtTtUuUuUuUuUuUuWwYyYZzZzZzs"

    // Windows-1252 replacements for numeric entities 0x80...0x9F. Go's html.unescapeEntity.
    private static let cp1252: [UInt32] = [
        0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
        0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
        0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
        0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
    ]

    // HTML5 named characters, one "name hex" line, as Go's encoding/html entity map
    // (the semicolon is part of the name when the map has it). folds decodes with
    // html.UnescapeString. A drift from that table is a bug.
    private static let entityText = #"""
AElig; 000000C6
AMP; 00000026
Aacute; 000000C1
Abreve; 00000102
Acirc; 000000C2
Acy; 00000410
Afr; 0001D504
Agrave; 000000C0
Alpha; 00000391
Amacr; 00000100
And; 00002A53
Aogon; 00000104
Aopf; 0001D538
ApplyFunction; 00002061
Aring; 000000C5
Ascr; 0001D49C
Assign; 00002254
Atilde; 000000C3
Auml; 000000C4
Backslash; 00002216
Barv; 00002AE7
Barwed; 00002306
Bcy; 00000411
Because; 00002235
Bernoullis; 0000212C
Beta; 00000392
Bfr; 0001D505
Bopf; 0001D539
Breve; 000002D8
Bscr; 0000212C
Bumpeq; 0000224E
CHcy; 00000427
COPY; 000000A9
Cacute; 00000106
Cap; 000022D2
CapitalDifferentialD; 00002145
Cayleys; 0000212D
Ccaron; 0000010C
Ccedil; 000000C7
Ccirc; 00000108
Cconint; 00002230
Cdot; 0000010A
Cedilla; 000000B8
CenterDot; 000000B7
Cfr; 0000212D
Chi; 000003A7
CircleDot; 00002299
CircleMinus; 00002296
CirclePlus; 00002295
CircleTimes; 00002297
ClockwiseContourIntegral; 00002232
CloseCurlyDoubleQuote; 0000201D
CloseCurlyQuote; 00002019
Colon; 00002237
Colone; 00002A74
Congruent; 00002261
Conint; 0000222F
ContourIntegral; 0000222E
Copf; 00002102
Coproduct; 00002210
CounterClockwiseContourIntegral; 00002233
Cross; 00002A2F
Cscr; 0001D49E
Cup; 000022D3
CupCap; 0000224D
DD; 00002145
DDotrahd; 00002911
DJcy; 00000402
DScy; 00000405
DZcy; 0000040F
Dagger; 00002021
Darr; 000021A1
Dashv; 00002AE4
Dcaron; 0000010E
Dcy; 00000414
Del; 00002207
Delta; 00000394
Dfr; 0001D507
DiacriticalAcute; 000000B4
DiacriticalDot; 000002D9
DiacriticalDoubleAcute; 000002DD
DiacriticalGrave; 00000060
DiacriticalTilde; 000002DC
Diamond; 000022C4
DifferentialD; 00002146
Dopf; 0001D53B
Dot; 000000A8
DotDot; 000020DC
DotEqual; 00002250
DoubleContourIntegral; 0000222F
DoubleDot; 000000A8
DoubleDownArrow; 000021D3
DoubleLeftArrow; 000021D0
DoubleLeftRightArrow; 000021D4
DoubleLeftTee; 00002AE4
DoubleLongLeftArrow; 000027F8
DoubleLongLeftRightArrow; 000027FA
DoubleLongRightArrow; 000027F9
DoubleRightArrow; 000021D2
DoubleRightTee; 000022A8
DoubleUpArrow; 000021D1
DoubleUpDownArrow; 000021D5
DoubleVerticalBar; 00002225
DownArrow; 00002193
DownArrowBar; 00002913
DownArrowUpArrow; 000021F5
DownBreve; 00000311
DownLeftRightVector; 00002950
DownLeftTeeVector; 0000295E
DownLeftVector; 000021BD
DownLeftVectorBar; 00002956
DownRightTeeVector; 0000295F
DownRightVector; 000021C1
DownRightVectorBar; 00002957
DownTee; 000022A4
DownTeeArrow; 000021A7
Downarrow; 000021D3
Dscr; 0001D49F
Dstrok; 00000110
ENG; 0000014A
ETH; 000000D0
Eacute; 000000C9
Ecaron; 0000011A
Ecirc; 000000CA
Ecy; 0000042D
Edot; 00000116
Efr; 0001D508
Egrave; 000000C8
Element; 00002208
Emacr; 00000112
EmptySmallSquare; 000025FB
EmptyVerySmallSquare; 000025AB
Eogon; 00000118
Eopf; 0001D53C
Epsilon; 00000395
Equal; 00002A75
EqualTilde; 00002242
Equilibrium; 000021CC
Escr; 00002130
Esim; 00002A73
Eta; 00000397
Euml; 000000CB
Exists; 00002203
ExponentialE; 00002147
Fcy; 00000424
Ffr; 0001D509
FilledSmallSquare; 000025FC
FilledVerySmallSquare; 000025AA
Fopf; 0001D53D
ForAll; 00002200
Fouriertrf; 00002131
Fscr; 00002131
GJcy; 00000403
GT; 0000003E
Gamma; 00000393
Gammad; 000003DC
Gbreve; 0000011E
Gcedil; 00000122
Gcirc; 0000011C
Gcy; 00000413
Gdot; 00000120
Gfr; 0001D50A
Gg; 000022D9
Gopf; 0001D53E
GreaterEqual; 00002265
GreaterEqualLess; 000022DB
GreaterFullEqual; 00002267
GreaterGreater; 00002AA2
GreaterLess; 00002277
GreaterSlantEqual; 00002A7E
GreaterTilde; 00002273
Gscr; 0001D4A2
Gt; 0000226B
HARDcy; 0000042A
Hacek; 000002C7
Hat; 0000005E
Hcirc; 00000124
Hfr; 0000210C
HilbertSpace; 0000210B
Hopf; 0000210D
HorizontalLine; 00002500
Hscr; 0000210B
Hstrok; 00000126
HumpDownHump; 0000224E
HumpEqual; 0000224F
IEcy; 00000415
IJlig; 00000132
IOcy; 00000401
Iacute; 000000CD
Icirc; 000000CE
Icy; 00000418
Idot; 00000130
Ifr; 00002111
Igrave; 000000CC
Im; 00002111
Imacr; 0000012A
ImaginaryI; 00002148
Implies; 000021D2
Int; 0000222C
Integral; 0000222B
Intersection; 000022C2
InvisibleComma; 00002063
InvisibleTimes; 00002062
Iogon; 0000012E
Iopf; 0001D540
Iota; 00000399
Iscr; 00002110
Itilde; 00000128
Iukcy; 00000406
Iuml; 000000CF
Jcirc; 00000134
Jcy; 00000419
Jfr; 0001D50D
Jopf; 0001D541
Jscr; 0001D4A5
Jsercy; 00000408
Jukcy; 00000404
KHcy; 00000425
KJcy; 0000040C
Kappa; 0000039A
Kcedil; 00000136
Kcy; 0000041A
Kfr; 0001D50E
Kopf; 0001D542
Kscr; 0001D4A6
LJcy; 00000409
LT; 0000003C
Lacute; 00000139
Lambda; 0000039B
Lang; 000027EA
Laplacetrf; 00002112
Larr; 0000219E
Lcaron; 0000013D
Lcedil; 0000013B
Lcy; 0000041B
LeftAngleBracket; 000027E8
LeftArrow; 00002190
LeftArrowBar; 000021E4
LeftArrowRightArrow; 000021C6
LeftCeiling; 00002308
LeftDoubleBracket; 000027E6
LeftDownTeeVector; 00002961
LeftDownVector; 000021C3
LeftDownVectorBar; 00002959
LeftFloor; 0000230A
LeftRightArrow; 00002194
LeftRightVector; 0000294E
LeftTee; 000022A3
LeftTeeArrow; 000021A4
LeftTeeVector; 0000295A
LeftTriangle; 000022B2
LeftTriangleBar; 000029CF
LeftTriangleEqual; 000022B4
LeftUpDownVector; 00002951
LeftUpTeeVector; 00002960
LeftUpVector; 000021BF
LeftUpVectorBar; 00002958
LeftVector; 000021BC
LeftVectorBar; 00002952
Leftarrow; 000021D0
Leftrightarrow; 000021D4
LessEqualGreater; 000022DA
LessFullEqual; 00002266
LessGreater; 00002276
LessLess; 00002AA1
LessSlantEqual; 00002A7D
LessTilde; 00002272
Lfr; 0001D50F
Ll; 000022D8
Lleftarrow; 000021DA
Lmidot; 0000013F
LongLeftArrow; 000027F5
LongLeftRightArrow; 000027F7
LongRightArrow; 000027F6
Longleftarrow; 000027F8
Longleftrightarrow; 000027FA
Longrightarrow; 000027F9
Lopf; 0001D543
LowerLeftArrow; 00002199
LowerRightArrow; 00002198
Lscr; 00002112
Lsh; 000021B0
Lstrok; 00000141
Lt; 0000226A
Map; 00002905
Mcy; 0000041C
MediumSpace; 0000205F
Mellintrf; 00002133
Mfr; 0001D510
MinusPlus; 00002213
Mopf; 0001D544
Mscr; 00002133
Mu; 0000039C
NJcy; 0000040A
Nacute; 00000143
Ncaron; 00000147
Ncedil; 00000145
Ncy; 0000041D
NegativeMediumSpace; 0000200B
NegativeThickSpace; 0000200B
NegativeThinSpace; 0000200B
NegativeVeryThinSpace; 0000200B
NestedGreaterGreater; 0000226B
NestedLessLess; 0000226A
NewLine; 0000000A
Nfr; 0001D511
NoBreak; 00002060
NonBreakingSpace; 000000A0
Nopf; 00002115
Not; 00002AEC
NotCongruent; 00002262
NotCupCap; 0000226D
NotDoubleVerticalBar; 00002226
NotElement; 00002209
NotEqual; 00002260
NotExists; 00002204
NotGreater; 0000226F
NotGreaterEqual; 00002271
NotGreaterLess; 00002279
NotGreaterTilde; 00002275
NotLeftTriangle; 000022EA
NotLeftTriangleEqual; 000022EC
NotLess; 0000226E
NotLessEqual; 00002270
NotLessGreater; 00002278
NotLessTilde; 00002274
NotPrecedes; 00002280
NotPrecedesSlantEqual; 000022E0
NotReverseElement; 0000220C
NotRightTriangle; 000022EB
NotRightTriangleEqual; 000022ED
NotSquareSubsetEqual; 000022E2
NotSquareSupersetEqual; 000022E3
NotSubsetEqual; 00002288
NotSucceeds; 00002281
NotSucceedsSlantEqual; 000022E1
NotSupersetEqual; 00002289
NotTilde; 00002241
NotTildeEqual; 00002244
NotTildeFullEqual; 00002247
NotTildeTilde; 00002249
NotVerticalBar; 00002224
Nscr; 0001D4A9
Ntilde; 000000D1
Nu; 0000039D
OElig; 00000152
Oacute; 000000D3
Ocirc; 000000D4
Ocy; 0000041E
Odblac; 00000150
Ofr; 0001D512
Ograve; 000000D2
Omacr; 0000014C
Omega; 000003A9
Omicron; 0000039F
Oopf; 0001D546
OpenCurlyDoubleQuote; 0000201C
OpenCurlyQuote; 00002018
Or; 00002A54
Oscr; 0001D4AA
Oslash; 000000D8
Otilde; 000000D5
Otimes; 00002A37
Ouml; 000000D6
OverBar; 0000203E
OverBrace; 000023DE
OverBracket; 000023B4
OverParenthesis; 000023DC
PartialD; 00002202
Pcy; 0000041F
Pfr; 0001D513
Phi; 000003A6
Pi; 000003A0
PlusMinus; 000000B1
Poincareplane; 0000210C
Popf; 00002119
Pr; 00002ABB
Precedes; 0000227A
PrecedesEqual; 00002AAF
PrecedesSlantEqual; 0000227C
PrecedesTilde; 0000227E
Prime; 00002033
Product; 0000220F
Proportion; 00002237
Proportional; 0000221D
Pscr; 0001D4AB
Psi; 000003A8
QUOT; 00000022
Qfr; 0001D514
Qopf; 0000211A
Qscr; 0001D4AC
RBarr; 00002910
REG; 000000AE
Racute; 00000154
Rang; 000027EB
Rarr; 000021A0
Rarrtl; 00002916
Rcaron; 00000158
Rcedil; 00000156
Rcy; 00000420
Re; 0000211C
ReverseElement; 0000220B
ReverseEquilibrium; 000021CB
ReverseUpEquilibrium; 0000296F
Rfr; 0000211C
Rho; 000003A1
RightAngleBracket; 000027E9
RightArrow; 00002192
RightArrowBar; 000021E5
RightArrowLeftArrow; 000021C4
RightCeiling; 00002309
RightDoubleBracket; 000027E7
RightDownTeeVector; 0000295D
RightDownVector; 000021C2
RightDownVectorBar; 00002955
RightFloor; 0000230B
RightTee; 000022A2
RightTeeArrow; 000021A6
RightTeeVector; 0000295B
RightTriangle; 000022B3
RightTriangleBar; 000029D0
RightTriangleEqual; 000022B5
RightUpDownVector; 0000294F
RightUpTeeVector; 0000295C
RightUpVector; 000021BE
RightUpVectorBar; 00002954
RightVector; 000021C0
RightVectorBar; 00002953
Rightarrow; 000021D2
Ropf; 0000211D
RoundImplies; 00002970
Rrightarrow; 000021DB
Rscr; 0000211B
Rsh; 000021B1
RuleDelayed; 000029F4
SHCHcy; 00000429
SHcy; 00000428
SOFTcy; 0000042C
Sacute; 0000015A
Sc; 00002ABC
Scaron; 00000160
Scedil; 0000015E
Scirc; 0000015C
Scy; 00000421
Sfr; 0001D516
ShortDownArrow; 00002193
ShortLeftArrow; 00002190
ShortRightArrow; 00002192
ShortUpArrow; 00002191
Sigma; 000003A3
SmallCircle; 00002218
Sopf; 0001D54A
Sqrt; 0000221A
Square; 000025A1
SquareIntersection; 00002293
SquareSubset; 0000228F
SquareSubsetEqual; 00002291
SquareSuperset; 00002290
SquareSupersetEqual; 00002292
SquareUnion; 00002294
Sscr; 0001D4AE
Star; 000022C6
Sub; 000022D0
Subset; 000022D0
SubsetEqual; 00002286
Succeeds; 0000227B
SucceedsEqual; 00002AB0
SucceedsSlantEqual; 0000227D
SucceedsTilde; 0000227F
SuchThat; 0000220B
Sum; 00002211
Sup; 000022D1
Superset; 00002283
SupersetEqual; 00002287
Supset; 000022D1
THORN; 000000DE
TRADE; 00002122
TSHcy; 0000040B
TScy; 00000426
Tab; 00000009
Tau; 000003A4
Tcaron; 00000164
Tcedil; 00000162
Tcy; 00000422
Tfr; 0001D517
Therefore; 00002234
Theta; 00000398
ThinSpace; 00002009
Tilde; 0000223C
TildeEqual; 00002243
TildeFullEqual; 00002245
TildeTilde; 00002248
Topf; 0001D54B
TripleDot; 000020DB
Tscr; 0001D4AF
Tstrok; 00000166
Uacute; 000000DA
Uarr; 0000219F
Uarrocir; 00002949
Ubrcy; 0000040E
Ubreve; 0000016C
Ucirc; 000000DB
Ucy; 00000423
Udblac; 00000170
Ufr; 0001D518
Ugrave; 000000D9
Umacr; 0000016A
UnderBar; 0000005F
UnderBrace; 000023DF
UnderBracket; 000023B5
UnderParenthesis; 000023DD
Union; 000022C3
UnionPlus; 0000228E
Uogon; 00000172
Uopf; 0001D54C
UpArrow; 00002191
UpArrowBar; 00002912
UpArrowDownArrow; 000021C5
UpDownArrow; 00002195
UpEquilibrium; 0000296E
UpTee; 000022A5
UpTeeArrow; 000021A5
Uparrow; 000021D1
Updownarrow; 000021D5
UpperLeftArrow; 00002196
UpperRightArrow; 00002197
Upsi; 000003D2
Upsilon; 000003A5
Uring; 0000016E
Uscr; 0001D4B0
Utilde; 00000168
Uuml; 000000DC
VDash; 000022AB
Vbar; 00002AEB
Vcy; 00000412
Vdash; 000022A9
Vdashl; 00002AE6
Vee; 000022C1
Verbar; 00002016
Vert; 00002016
VerticalBar; 00002223
VerticalLine; 0000007C
VerticalSeparator; 00002758
VerticalTilde; 00002240
VeryThinSpace; 0000200A
Vfr; 0001D519
Vopf; 0001D54D
Vscr; 0001D4B1
Vvdash; 000022AA
Wcirc; 00000174
Wedge; 000022C0
Wfr; 0001D51A
Wopf; 0001D54E
Wscr; 0001D4B2
Xfr; 0001D51B
Xi; 0000039E
Xopf; 0001D54F
Xscr; 0001D4B3
YAcy; 0000042F
YIcy; 00000407
YUcy; 0000042E
Yacute; 000000DD
Ycirc; 00000176
Ycy; 0000042B
Yfr; 0001D51C
Yopf; 0001D550
Yscr; 0001D4B4
Yuml; 00000178
ZHcy; 00000416
Zacute; 00000179
Zcaron; 0000017D
Zcy; 00000417
Zdot; 0000017B
ZeroWidthSpace; 0000200B
Zeta; 00000396
Zfr; 00002128
Zopf; 00002124
Zscr; 0001D4B5
aacute; 000000E1
abreve; 00000103
ac; 0000223E
acd; 0000223F
acirc; 000000E2
acute; 000000B4
acy; 00000430
aelig; 000000E6
af; 00002061
afr; 0001D51E
agrave; 000000E0
alefsym; 00002135
aleph; 00002135
alpha; 000003B1
amacr; 00000101
amalg; 00002A3F
amp; 00000026
and; 00002227
andand; 00002A55
andd; 00002A5C
andslope; 00002A58
andv; 00002A5A
ang; 00002220
ange; 000029A4
angle; 00002220
angmsd; 00002221
angmsdaa; 000029A8
angmsdab; 000029A9
angmsdac; 000029AA
angmsdad; 000029AB
angmsdae; 000029AC
angmsdaf; 000029AD
angmsdag; 000029AE
angmsdah; 000029AF
angrt; 0000221F
angrtvb; 000022BE
angrtvbd; 0000299D
angsph; 00002222
angst; 000000C5
angzarr; 0000237C
aogon; 00000105
aopf; 0001D552
ap; 00002248
apE; 00002A70
apacir; 00002A6F
ape; 0000224A
apid; 0000224B
apos; 00000027
approx; 00002248
approxeq; 0000224A
aring; 000000E5
ascr; 0001D4B6
ast; 0000002A
asymp; 00002248
asympeq; 0000224D
atilde; 000000E3
auml; 000000E4
awconint; 00002233
awint; 00002A11
bNot; 00002AED
backcong; 0000224C
backepsilon; 000003F6
backprime; 00002035
backsim; 0000223D
backsimeq; 000022CD
barvee; 000022BD
barwed; 00002305
barwedge; 00002305
bbrk; 000023B5
bbrktbrk; 000023B6
bcong; 0000224C
bcy; 00000431
bdquo; 0000201E
becaus; 00002235
because; 00002235
bemptyv; 000029B0
bepsi; 000003F6
bernou; 0000212C
beta; 000003B2
beth; 00002136
between; 0000226C
bfr; 0001D51F
bigcap; 000022C2
bigcirc; 000025EF
bigcup; 000022C3
bigodot; 00002A00
bigoplus; 00002A01
bigotimes; 00002A02
bigsqcup; 00002A06
bigstar; 00002605
bigtriangledown; 000025BD
bigtriangleup; 000025B3
biguplus; 00002A04
bigvee; 000022C1
bigwedge; 000022C0
bkarow; 0000290D
blacklozenge; 000029EB
blacksquare; 000025AA
blacktriangle; 000025B4
blacktriangledown; 000025BE
blacktriangleleft; 000025C2
blacktriangleright; 000025B8
blank; 00002423
blk12; 00002592
blk14; 00002591
blk34; 00002593
block; 00002588
bnot; 00002310
bopf; 0001D553
bot; 000022A5
bottom; 000022A5
bowtie; 000022C8
boxDL; 00002557
boxDR; 00002554
boxDl; 00002556
boxDr; 00002553
boxH; 00002550
boxHD; 00002566
boxHU; 00002569
boxHd; 00002564
boxHu; 00002567
boxUL; 0000255D
boxUR; 0000255A
boxUl; 0000255C
boxUr; 00002559
boxV; 00002551
boxVH; 0000256C
boxVL; 00002563
boxVR; 00002560
boxVh; 0000256B
boxVl; 00002562
boxVr; 0000255F
boxbox; 000029C9
boxdL; 00002555
boxdR; 00002552
boxdl; 00002510
boxdr; 0000250C
boxh; 00002500
boxhD; 00002565
boxhU; 00002568
boxhd; 0000252C
boxhu; 00002534
boxminus; 0000229F
boxplus; 0000229E
boxtimes; 000022A0
boxuL; 0000255B
boxuR; 00002558
boxul; 00002518
boxur; 00002514
boxv; 00002502
boxvH; 0000256A
boxvL; 00002561
boxvR; 0000255E
boxvh; 0000253C
boxvl; 00002524
boxvr; 0000251C
bprime; 00002035
breve; 000002D8
brvbar; 000000A6
bscr; 0001D4B7
bsemi; 0000204F
bsim; 0000223D
bsime; 000022CD
bsol; 0000005C
bsolb; 000029C5
bsolhsub; 000027C8
bull; 00002022
bullet; 00002022
bump; 0000224E
bumpE; 00002AAE
bumpe; 0000224F
bumpeq; 0000224F
cacute; 00000107
cap; 00002229
capand; 00002A44
capbrcup; 00002A49
capcap; 00002A4B
capcup; 00002A47
capdot; 00002A40
caret; 00002041
caron; 000002C7
ccaps; 00002A4D
ccaron; 0000010D
ccedil; 000000E7
ccirc; 00000109
ccups; 00002A4C
ccupssm; 00002A50
cdot; 0000010B
cedil; 000000B8
cemptyv; 000029B2
cent; 000000A2
centerdot; 000000B7
cfr; 0001D520
chcy; 00000447
check; 00002713
checkmark; 00002713
chi; 000003C7
cir; 000025CB
cirE; 000029C3
circ; 000002C6
circeq; 00002257
circlearrowleft; 000021BA
circlearrowright; 000021BB
circledR; 000000AE
circledS; 000024C8
circledast; 0000229B
circledcirc; 0000229A
circleddash; 0000229D
cire; 00002257
cirfnint; 00002A10
cirmid; 00002AEF
cirscir; 000029C2
clubs; 00002663
clubsuit; 00002663
colon; 0000003A
colone; 00002254
coloneq; 00002254
comma; 0000002C
commat; 00000040
comp; 00002201
compfn; 00002218
complement; 00002201
complexes; 00002102
cong; 00002245
congdot; 00002A6D
conint; 0000222E
copf; 0001D554
coprod; 00002210
copy; 000000A9
copysr; 00002117
crarr; 000021B5
cross; 00002717
cscr; 0001D4B8
csub; 00002ACF
csube; 00002AD1
csup; 00002AD0
csupe; 00002AD2
ctdot; 000022EF
cudarrl; 00002938
cudarrr; 00002935
cuepr; 000022DE
cuesc; 000022DF
cularr; 000021B6
cularrp; 0000293D
cup; 0000222A
cupbrcap; 00002A48
cupcap; 00002A46
cupcup; 00002A4A
cupdot; 0000228D
cupor; 00002A45
curarr; 000021B7
curarrm; 0000293C
curlyeqprec; 000022DE
curlyeqsucc; 000022DF
curlyvee; 000022CE
curlywedge; 000022CF
curren; 000000A4
curvearrowleft; 000021B6
curvearrowright; 000021B7
cuvee; 000022CE
cuwed; 000022CF
cwconint; 00002232
cwint; 00002231
cylcty; 0000232D
dArr; 000021D3
dHar; 00002965
dagger; 00002020
daleth; 00002138
darr; 00002193
dash; 00002010
dashv; 000022A3
dbkarow; 0000290F
dblac; 000002DD
dcaron; 0000010F
dcy; 00000434
dd; 00002146
ddagger; 00002021
ddarr; 000021CA
ddotseq; 00002A77
deg; 000000B0
delta; 000003B4
demptyv; 000029B1
dfisht; 0000297F
dfr; 0001D521
dharl; 000021C3
dharr; 000021C2
diam; 000022C4
diamond; 000022C4
diamondsuit; 00002666
diams; 00002666
die; 000000A8
digamma; 000003DD
disin; 000022F2
div; 000000F7
divide; 000000F7
divideontimes; 000022C7
divonx; 000022C7
djcy; 00000452
dlcorn; 0000231E
dlcrop; 0000230D
dollar; 00000024
dopf; 0001D555
dot; 000002D9
doteq; 00002250
doteqdot; 00002251
dotminus; 00002238
dotplus; 00002214
dotsquare; 000022A1
doublebarwedge; 00002306
downarrow; 00002193
downdownarrows; 000021CA
downharpoonleft; 000021C3
downharpoonright; 000021C2
drbkarow; 00002910
drcorn; 0000231F
drcrop; 0000230C
dscr; 0001D4B9
dscy; 00000455
dsol; 000029F6
dstrok; 00000111
dtdot; 000022F1
dtri; 000025BF
dtrif; 000025BE
duarr; 000021F5
duhar; 0000296F
dwangle; 000029A6
dzcy; 0000045F
dzigrarr; 000027FF
eDDot; 00002A77
eDot; 00002251
eacute; 000000E9
easter; 00002A6E
ecaron; 0000011B
ecir; 00002256
ecirc; 000000EA
ecolon; 00002255
ecy; 0000044D
edot; 00000117
ee; 00002147
efDot; 00002252
efr; 0001D522
eg; 00002A9A
egrave; 000000E8
egs; 00002A96
egsdot; 00002A98
el; 00002A99
elinters; 000023E7
ell; 00002113
els; 00002A95
elsdot; 00002A97
emacr; 00000113
empty; 00002205
emptyset; 00002205
emptyv; 00002205
emsp; 00002003
emsp13; 00002004
emsp14; 00002005
eng; 0000014B
ensp; 00002002
eogon; 00000119
eopf; 0001D556
epar; 000022D5
eparsl; 000029E3
eplus; 00002A71
epsi; 000003B5
epsilon; 000003B5
epsiv; 000003F5
eqcirc; 00002256
eqcolon; 00002255
eqsim; 00002242
eqslantgtr; 00002A96
eqslantless; 00002A95
equals; 0000003D
equest; 0000225F
equiv; 00002261
equivDD; 00002A78
eqvparsl; 000029E5
erDot; 00002253
erarr; 00002971
escr; 0000212F
esdot; 00002250
esim; 00002242
eta; 000003B7
eth; 000000F0
euml; 000000EB
euro; 000020AC
excl; 00000021
exist; 00002203
expectation; 00002130
exponentiale; 00002147
fallingdotseq; 00002252
fcy; 00000444
female; 00002640
ffilig; 0000FB03
fflig; 0000FB00
ffllig; 0000FB04
ffr; 0001D523
filig; 0000FB01
flat; 0000266D
fllig; 0000FB02
fltns; 000025B1
fnof; 00000192
fopf; 0001D557
forall; 00002200
fork; 000022D4
forkv; 00002AD9
fpartint; 00002A0D
frac12; 000000BD
frac13; 00002153
frac14; 000000BC
frac15; 00002155
frac16; 00002159
frac18; 0000215B
frac23; 00002154
frac25; 00002156
frac34; 000000BE
frac35; 00002157
frac38; 0000215C
frac45; 00002158
frac56; 0000215A
frac58; 0000215D
frac78; 0000215E
frasl; 00002044
frown; 00002322
fscr; 0001D4BB
gE; 00002267
gEl; 00002A8C
gacute; 000001F5
gamma; 000003B3
gammad; 000003DD
gap; 00002A86
gbreve; 0000011F
gcirc; 0000011D
gcy; 00000433
gdot; 00000121
ge; 00002265
gel; 000022DB
geq; 00002265
geqq; 00002267
geqslant; 00002A7E
ges; 00002A7E
gescc; 00002AA9
gesdot; 00002A80
gesdoto; 00002A82
gesdotol; 00002A84
gesles; 00002A94
gfr; 0001D524
gg; 0000226B
ggg; 000022D9
gimel; 00002137
gjcy; 00000453
gl; 00002277
glE; 00002A92
gla; 00002AA5
glj; 00002AA4
gnE; 00002269
gnap; 00002A8A
gnapprox; 00002A8A
gne; 00002A88
gneq; 00002A88
gneqq; 00002269
gnsim; 000022E7
gopf; 0001D558
grave; 00000060
gscr; 0000210A
gsim; 00002273
gsime; 00002A8E
gsiml; 00002A90
gt; 0000003E
gtcc; 00002AA7
gtcir; 00002A7A
gtdot; 000022D7
gtlPar; 00002995
gtquest; 00002A7C
gtrapprox; 00002A86
gtrarr; 00002978
gtrdot; 000022D7
gtreqless; 000022DB
gtreqqless; 00002A8C
gtrless; 00002277
gtrsim; 00002273
hArr; 000021D4
hairsp; 0000200A
half; 000000BD
hamilt; 0000210B
hardcy; 0000044A
harr; 00002194
harrcir; 00002948
harrw; 000021AD
hbar; 0000210F
hcirc; 00000125
hearts; 00002665
heartsuit; 00002665
hellip; 00002026
hercon; 000022B9
hfr; 0001D525
hksearow; 00002925
hkswarow; 00002926
hoarr; 000021FF
homtht; 0000223B
hookleftarrow; 000021A9
hookrightarrow; 000021AA
hopf; 0001D559
horbar; 00002015
hscr; 0001D4BD
hslash; 0000210F
hstrok; 00000127
hybull; 00002043
hyphen; 00002010
iacute; 000000ED
ic; 00002063
icirc; 000000EE
icy; 00000438
iecy; 00000435
iexcl; 000000A1
iff; 000021D4
ifr; 0001D526
igrave; 000000EC
ii; 00002148
iiiint; 00002A0C
iiint; 0000222D
iinfin; 000029DC
iiota; 00002129
ijlig; 00000133
imacr; 0000012B
image; 00002111
imagline; 00002110
imagpart; 00002111
imath; 00000131
imof; 000022B7
imped; 000001B5
in; 00002208
incare; 00002105
infin; 0000221E
infintie; 000029DD
inodot; 00000131
int; 0000222B
intcal; 000022BA
integers; 00002124
intercal; 000022BA
intlarhk; 00002A17
intprod; 00002A3C
iocy; 00000451
iogon; 0000012F
iopf; 0001D55A
iota; 000003B9
iprod; 00002A3C
iquest; 000000BF
iscr; 0001D4BE
isin; 00002208
isinE; 000022F9
isindot; 000022F5
isins; 000022F4
isinsv; 000022F3
isinv; 00002208
it; 00002062
itilde; 00000129
iukcy; 00000456
iuml; 000000EF
jcirc; 00000135
jcy; 00000439
jfr; 0001D527
jmath; 00000237
jopf; 0001D55B
jscr; 0001D4BF
jsercy; 00000458
jukcy; 00000454
kappa; 000003BA
kappav; 000003F0
kcedil; 00000137
kcy; 0000043A
kfr; 0001D528
kgreen; 00000138
khcy; 00000445
kjcy; 0000045C
kopf; 0001D55C
kscr; 0001D4C0
lAarr; 000021DA
lArr; 000021D0
lAtail; 0000291B
lBarr; 0000290E
lE; 00002266
lEg; 00002A8B
lHar; 00002962
lacute; 0000013A
laemptyv; 000029B4
lagran; 00002112
lambda; 000003BB
lang; 000027E8
langd; 00002991
langle; 000027E8
lap; 00002A85
laquo; 000000AB
larr; 00002190
larrb; 000021E4
larrbfs; 0000291F
larrfs; 0000291D
larrhk; 000021A9
larrlp; 000021AB
larrpl; 00002939
larrsim; 00002973
larrtl; 000021A2
lat; 00002AAB
latail; 00002919
late; 00002AAD
lbarr; 0000290C
lbbrk; 00002772
lbrace; 0000007B
lbrack; 0000005B
lbrke; 0000298B
lbrksld; 0000298F
lbrkslu; 0000298D
lcaron; 0000013E
lcedil; 0000013C
lceil; 00002308
lcub; 0000007B
lcy; 0000043B
ldca; 00002936
ldquo; 0000201C
ldquor; 0000201E
ldrdhar; 00002967
ldrushar; 0000294B
ldsh; 000021B2
le; 00002264
leftarrow; 00002190
leftarrowtail; 000021A2
leftharpoondown; 000021BD
leftharpoonup; 000021BC
leftleftarrows; 000021C7
leftrightarrow; 00002194
leftrightarrows; 000021C6
leftrightharpoons; 000021CB
leftrightsquigarrow; 000021AD
leftthreetimes; 000022CB
leg; 000022DA
leq; 00002264
leqq; 00002266
leqslant; 00002A7D
les; 00002A7D
lescc; 00002AA8
lesdot; 00002A7F
lesdoto; 00002A81
lesdotor; 00002A83
lesges; 00002A93
lessapprox; 00002A85
lessdot; 000022D6
lesseqgtr; 000022DA
lesseqqgtr; 00002A8B
lessgtr; 00002276
lesssim; 00002272
lfisht; 0000297C
lfloor; 0000230A
lfr; 0001D529
lg; 00002276
lgE; 00002A91
lhard; 000021BD
lharu; 000021BC
lharul; 0000296A
lhblk; 00002584
ljcy; 00000459
ll; 0000226A
llarr; 000021C7
llcorner; 0000231E
llhard; 0000296B
lltri; 000025FA
lmidot; 00000140
lmoust; 000023B0
lmoustache; 000023B0
lnE; 00002268
lnap; 00002A89
lnapprox; 00002A89
lne; 00002A87
lneq; 00002A87
lneqq; 00002268
lnsim; 000022E6
loang; 000027EC
loarr; 000021FD
lobrk; 000027E6
longleftarrow; 000027F5
longleftrightarrow; 000027F7
longmapsto; 000027FC
longrightarrow; 000027F6
looparrowleft; 000021AB
looparrowright; 000021AC
lopar; 00002985
lopf; 0001D55D
loplus; 00002A2D
lotimes; 00002A34
lowast; 00002217
lowbar; 0000005F
loz; 000025CA
lozenge; 000025CA
lozf; 000029EB
lpar; 00000028
lparlt; 00002993
lrarr; 000021C6
lrcorner; 0000231F
lrhar; 000021CB
lrhard; 0000296D
lrm; 0000200E
lrtri; 000022BF
lsaquo; 00002039
lscr; 0001D4C1
lsh; 000021B0
lsim; 00002272
lsime; 00002A8D
lsimg; 00002A8F
lsqb; 0000005B
lsquo; 00002018
lsquor; 0000201A
lstrok; 00000142
lt; 0000003C
ltcc; 00002AA6
ltcir; 00002A79
ltdot; 000022D6
lthree; 000022CB
ltimes; 000022C9
ltlarr; 00002976
ltquest; 00002A7B
ltrPar; 00002996
ltri; 000025C3
ltrie; 000022B4
ltrif; 000025C2
lurdshar; 0000294A
luruhar; 00002966
mDDot; 0000223A
macr; 000000AF
male; 00002642
malt; 00002720
maltese; 00002720
map; 000021A6
mapsto; 000021A6
mapstodown; 000021A7
mapstoleft; 000021A4
mapstoup; 000021A5
marker; 000025AE
mcomma; 00002A29
mcy; 0000043C
mdash; 00002014
measuredangle; 00002221
mfr; 0001D52A
mho; 00002127
micro; 000000B5
mid; 00002223
midast; 0000002A
midcir; 00002AF0
middot; 000000B7
minus; 00002212
minusb; 0000229F
minusd; 00002238
minusdu; 00002A2A
mlcp; 00002ADB
mldr; 00002026
mnplus; 00002213
models; 000022A7
mopf; 0001D55E
mp; 00002213
mscr; 0001D4C2
mstpos; 0000223E
mu; 000003BC
multimap; 000022B8
mumap; 000022B8
nLeftarrow; 000021CD
nLeftrightarrow; 000021CE
nRightarrow; 000021CF
nVDash; 000022AF
nVdash; 000022AE
nabla; 00002207
nacute; 00000144
nap; 00002249
napos; 00000149
napprox; 00002249
natur; 0000266E
natural; 0000266E
naturals; 00002115
nbsp; 000000A0
ncap; 00002A43
ncaron; 00000148
ncedil; 00000146
ncong; 00002247
ncup; 00002A42
ncy; 0000043D
ndash; 00002013
ne; 00002260
neArr; 000021D7
nearhk; 00002924
nearr; 00002197
nearrow; 00002197
nequiv; 00002262
nesear; 00002928
nexist; 00002204
nexists; 00002204
nfr; 0001D52B
nge; 00002271
ngeq; 00002271
ngsim; 00002275
ngt; 0000226F
ngtr; 0000226F
nhArr; 000021CE
nharr; 000021AE
nhpar; 00002AF2
ni; 0000220B
nis; 000022FC
nisd; 000022FA
niv; 0000220B
njcy; 0000045A
nlArr; 000021CD
nlarr; 0000219A
nldr; 00002025
nle; 00002270
nleftarrow; 0000219A
nleftrightarrow; 000021AE
nleq; 00002270
nless; 0000226E
nlsim; 00002274
nlt; 0000226E
nltri; 000022EA
nltrie; 000022EC
nmid; 00002224
nopf; 0001D55F
not; 000000AC
notin; 00002209
notinva; 00002209
notinvb; 000022F7
notinvc; 000022F6
notni; 0000220C
notniva; 0000220C
notnivb; 000022FE
notnivc; 000022FD
npar; 00002226
nparallel; 00002226
npolint; 00002A14
npr; 00002280
nprcue; 000022E0
nprec; 00002280
nrArr; 000021CF
nrarr; 0000219B
nrightarrow; 0000219B
nrtri; 000022EB
nrtrie; 000022ED
nsc; 00002281
nsccue; 000022E1
nscr; 0001D4C3
nshortmid; 00002224
nshortparallel; 00002226
nsim; 00002241
nsime; 00002244
nsimeq; 00002244
nsmid; 00002224
nspar; 00002226
nsqsube; 000022E2
nsqsupe; 000022E3
nsub; 00002284
nsube; 00002288
nsubseteq; 00002288
nsucc; 00002281
nsup; 00002285
nsupe; 00002289
nsupseteq; 00002289
ntgl; 00002279
ntilde; 000000F1
ntlg; 00002278
ntriangleleft; 000022EA
ntrianglelefteq; 000022EC
ntriangleright; 000022EB
ntrianglerighteq; 000022ED
nu; 000003BD
num; 00000023
numero; 00002116
numsp; 00002007
nvDash; 000022AD
nvHarr; 00002904
nvdash; 000022AC
nvinfin; 000029DE
nvlArr; 00002902
nvrArr; 00002903
nwArr; 000021D6
nwarhk; 00002923
nwarr; 00002196
nwarrow; 00002196
nwnear; 00002927
oS; 000024C8
oacute; 000000F3
oast; 0000229B
ocir; 0000229A
ocirc; 000000F4
ocy; 0000043E
odash; 0000229D
odblac; 00000151
odiv; 00002A38
odot; 00002299
odsold; 000029BC
oelig; 00000153
ofcir; 000029BF
ofr; 0001D52C
ogon; 000002DB
ograve; 000000F2
ogt; 000029C1
ohbar; 000029B5
ohm; 000003A9
oint; 0000222E
olarr; 000021BA
olcir; 000029BE
olcross; 000029BB
oline; 0000203E
olt; 000029C0
omacr; 0000014D
omega; 000003C9
omicron; 000003BF
omid; 000029B6
ominus; 00002296
oopf; 0001D560
opar; 000029B7
operp; 000029B9
oplus; 00002295
or; 00002228
orarr; 000021BB
ord; 00002A5D
order; 00002134
orderof; 00002134
ordf; 000000AA
ordm; 000000BA
origof; 000022B6
oror; 00002A56
orslope; 00002A57
orv; 00002A5B
oscr; 00002134
oslash; 000000F8
osol; 00002298
otilde; 000000F5
otimes; 00002297
otimesas; 00002A36
ouml; 000000F6
ovbar; 0000233D
par; 00002225
para; 000000B6
parallel; 00002225
parsim; 00002AF3
parsl; 00002AFD
part; 00002202
pcy; 0000043F
percnt; 00000025
period; 0000002E
permil; 00002030
perp; 000022A5
pertenk; 00002031
pfr; 0001D52D
phi; 000003C6
phiv; 000003D5
phmmat; 00002133
phone; 0000260E
pi; 000003C0
pitchfork; 000022D4
piv; 000003D6
planck; 0000210F
planckh; 0000210E
plankv; 0000210F
plus; 0000002B
plusacir; 00002A23
plusb; 0000229E
pluscir; 00002A22
plusdo; 00002214
plusdu; 00002A25
pluse; 00002A72
plusmn; 000000B1
plussim; 00002A26
plustwo; 00002A27
pm; 000000B1
pointint; 00002A15
popf; 0001D561
pound; 000000A3
pr; 0000227A
prE; 00002AB3
prap; 00002AB7
prcue; 0000227C
pre; 00002AAF
prec; 0000227A
precapprox; 00002AB7
preccurlyeq; 0000227C
preceq; 00002AAF
precnapprox; 00002AB9
precneqq; 00002AB5
precnsim; 000022E8
precsim; 0000227E
prime; 00002032
primes; 00002119
prnE; 00002AB5
prnap; 00002AB9
prnsim; 000022E8
prod; 0000220F
profalar; 0000232E
profline; 00002312
profsurf; 00002313
prop; 0000221D
propto; 0000221D
prsim; 0000227E
prurel; 000022B0
pscr; 0001D4C5
psi; 000003C8
puncsp; 00002008
qfr; 0001D52E
qint; 00002A0C
qopf; 0001D562
qprime; 00002057
qscr; 0001D4C6
quaternions; 0000210D
quatint; 00002A16
quest; 0000003F
questeq; 0000225F
quot; 00000022
rAarr; 000021DB
rArr; 000021D2
rAtail; 0000291C
rBarr; 0000290F
rHar; 00002964
racute; 00000155
radic; 0000221A
raemptyv; 000029B3
rang; 000027E9
rangd; 00002992
range; 000029A5
rangle; 000027E9
raquo; 000000BB
rarr; 00002192
rarrap; 00002975
rarrb; 000021E5
rarrbfs; 00002920
rarrc; 00002933
rarrfs; 0000291E
rarrhk; 000021AA
rarrlp; 000021AC
rarrpl; 00002945
rarrsim; 00002974
rarrtl; 000021A3
rarrw; 0000219D
ratail; 0000291A
ratio; 00002236
rationals; 0000211A
rbarr; 0000290D
rbbrk; 00002773
rbrace; 0000007D
rbrack; 0000005D
rbrke; 0000298C
rbrksld; 0000298E
rbrkslu; 00002990
rcaron; 00000159
rcedil; 00000157
rceil; 00002309
rcub; 0000007D
rcy; 00000440
rdca; 00002937
rdldhar; 00002969
rdquo; 0000201D
rdquor; 0000201D
rdsh; 000021B3
real; 0000211C
realine; 0000211B
realpart; 0000211C
reals; 0000211D
rect; 000025AD
reg; 000000AE
rfisht; 0000297D
rfloor; 0000230B
rfr; 0001D52F
rhard; 000021C1
rharu; 000021C0
rharul; 0000296C
rho; 000003C1
rhov; 000003F1
rightarrow; 00002192
rightarrowtail; 000021A3
rightharpoondown; 000021C1
rightharpoonup; 000021C0
rightleftarrows; 000021C4
rightleftharpoons; 000021CC
rightrightarrows; 000021C9
rightsquigarrow; 0000219D
rightthreetimes; 000022CC
ring; 000002DA
risingdotseq; 00002253
rlarr; 000021C4
rlhar; 000021CC
rlm; 0000200F
rmoust; 000023B1
rmoustache; 000023B1
rnmid; 00002AEE
roang; 000027ED
roarr; 000021FE
robrk; 000027E7
ropar; 00002986
ropf; 0001D563
roplus; 00002A2E
rotimes; 00002A35
rpar; 00000029
rpargt; 00002994
rppolint; 00002A12
rrarr; 000021C9
rsaquo; 0000203A
rscr; 0001D4C7
rsh; 000021B1
rsqb; 0000005D
rsquo; 00002019
rsquor; 00002019
rthree; 000022CC
rtimes; 000022CA
rtri; 000025B9
rtrie; 000022B5
rtrif; 000025B8
rtriltri; 000029CE
ruluhar; 00002968
rx; 0000211E
sacute; 0000015B
sbquo; 0000201A
sc; 0000227B
scE; 00002AB4
scap; 00002AB8
scaron; 00000161
sccue; 0000227D
sce; 00002AB0
scedil; 0000015F
scirc; 0000015D
scnE; 00002AB6
scnap; 00002ABA
scnsim; 000022E9
scpolint; 00002A13
scsim; 0000227F
scy; 00000441
sdot; 000022C5
sdotb; 000022A1
sdote; 00002A66
seArr; 000021D8
searhk; 00002925
searr; 00002198
searrow; 00002198
sect; 000000A7
semi; 0000003B
seswar; 00002929
setminus; 00002216
setmn; 00002216
sext; 00002736
sfr; 0001D530
sfrown; 00002322
sharp; 0000266F
shchcy; 00000449
shcy; 00000448
shortmid; 00002223
shortparallel; 00002225
shy; 000000AD
sigma; 000003C3
sigmaf; 000003C2
sigmav; 000003C2
sim; 0000223C
simdot; 00002A6A
sime; 00002243
simeq; 00002243
simg; 00002A9E
simgE; 00002AA0
siml; 00002A9D
simlE; 00002A9F
simne; 00002246
simplus; 00002A24
simrarr; 00002972
slarr; 00002190
smallsetminus; 00002216
smashp; 00002A33
smeparsl; 000029E4
smid; 00002223
smile; 00002323
smt; 00002AAA
smte; 00002AAC
softcy; 0000044C
sol; 0000002F
solb; 000029C4
solbar; 0000233F
sopf; 0001D564
spades; 00002660
spadesuit; 00002660
spar; 00002225
sqcap; 00002293
sqcup; 00002294
sqsub; 0000228F
sqsube; 00002291
sqsubset; 0000228F
sqsubseteq; 00002291
sqsup; 00002290
sqsupe; 00002292
sqsupset; 00002290
sqsupseteq; 00002292
squ; 000025A1
square; 000025A1
squarf; 000025AA
squf; 000025AA
srarr; 00002192
sscr; 0001D4C8
ssetmn; 00002216
ssmile; 00002323
sstarf; 000022C6
star; 00002606
starf; 00002605
straightepsilon; 000003F5
straightphi; 000003D5
strns; 000000AF
sub; 00002282
subE; 00002AC5
subdot; 00002ABD
sube; 00002286
subedot; 00002AC3
submult; 00002AC1
subnE; 00002ACB
subne; 0000228A
subplus; 00002ABF
subrarr; 00002979
subset; 00002282
subseteq; 00002286
subseteqq; 00002AC5
subsetneq; 0000228A
subsetneqq; 00002ACB
subsim; 00002AC7
subsub; 00002AD5
subsup; 00002AD3
succ; 0000227B
succapprox; 00002AB8
succcurlyeq; 0000227D
succeq; 00002AB0
succnapprox; 00002ABA
succneqq; 00002AB6
succnsim; 000022E9
succsim; 0000227F
sum; 00002211
sung; 0000266A
sup; 00002283
sup1; 000000B9
sup2; 000000B2
sup3; 000000B3
supE; 00002AC6
supdot; 00002ABE
supdsub; 00002AD8
supe; 00002287
supedot; 00002AC4
suphsol; 000027C9
suphsub; 00002AD7
suplarr; 0000297B
supmult; 00002AC2
supnE; 00002ACC
supne; 0000228B
supplus; 00002AC0
supset; 00002283
supseteq; 00002287
supseteqq; 00002AC6
supsetneq; 0000228B
supsetneqq; 00002ACC
supsim; 00002AC8
supsub; 00002AD4
supsup; 00002AD6
swArr; 000021D9
swarhk; 00002926
swarr; 00002199
swarrow; 00002199
swnwar; 0000292A
szlig; 000000DF
target; 00002316
tau; 000003C4
tbrk; 000023B4
tcaron; 00000165
tcedil; 00000163
tcy; 00000442
tdot; 000020DB
telrec; 00002315
tfr; 0001D531
there4; 00002234
therefore; 00002234
theta; 000003B8
thetasym; 000003D1
thetav; 000003D1
thickapprox; 00002248
thicksim; 0000223C
thinsp; 00002009
thkap; 00002248
thksim; 0000223C
thorn; 000000FE
tilde; 000002DC
times; 000000D7
timesb; 000022A0
timesbar; 00002A31
timesd; 00002A30
tint; 0000222D
toea; 00002928
top; 000022A4
topbot; 00002336
topcir; 00002AF1
topf; 0001D565
topfork; 00002ADA
tosa; 00002929
tprime; 00002034
trade; 00002122
triangle; 000025B5
triangledown; 000025BF
triangleleft; 000025C3
trianglelefteq; 000022B4
triangleq; 0000225C
triangleright; 000025B9
trianglerighteq; 000022B5
tridot; 000025EC
trie; 0000225C
triminus; 00002A3A
triplus; 00002A39
trisb; 000029CD
tritime; 00002A3B
trpezium; 000023E2
tscr; 0001D4C9
tscy; 00000446
tshcy; 0000045B
tstrok; 00000167
twixt; 0000226C
twoheadleftarrow; 0000219E
twoheadrightarrow; 000021A0
uArr; 000021D1
uHar; 00002963
uacute; 000000FA
uarr; 00002191
ubrcy; 0000045E
ubreve; 0000016D
ucirc; 000000FB
ucy; 00000443
udarr; 000021C5
udblac; 00000171
udhar; 0000296E
ufisht; 0000297E
ufr; 0001D532
ugrave; 000000F9
uharl; 000021BF
uharr; 000021BE
uhblk; 00002580
ulcorn; 0000231C
ulcorner; 0000231C
ulcrop; 0000230F
ultri; 000025F8
umacr; 0000016B
uml; 000000A8
uogon; 00000173
uopf; 0001D566
uparrow; 00002191
updownarrow; 00002195
upharpoonleft; 000021BF
upharpoonright; 000021BE
uplus; 0000228E
upsi; 000003C5
upsih; 000003D2
upsilon; 000003C5
upuparrows; 000021C8
urcorn; 0000231D
urcorner; 0000231D
urcrop; 0000230E
uring; 0000016F
urtri; 000025F9
uscr; 0001D4CA
utdot; 000022F0
utilde; 00000169
utri; 000025B5
utrif; 000025B4
uuarr; 000021C8
uuml; 000000FC
uwangle; 000029A7
vArr; 000021D5
vBar; 00002AE8
vBarv; 00002AE9
vDash; 000022A8
vangrt; 0000299C
varepsilon; 000003F5
varkappa; 000003F0
varnothing; 00002205
varphi; 000003D5
varpi; 000003D6
varpropto; 0000221D
varr; 00002195
varrho; 000003F1
varsigma; 000003C2
vartheta; 000003D1
vartriangleleft; 000022B2
vartriangleright; 000022B3
vcy; 00000432
vdash; 000022A2
vee; 00002228
veebar; 000022BB
veeeq; 0000225A
vellip; 000022EE
verbar; 0000007C
vert; 0000007C
vfr; 0001D533
vltri; 000022B2
vopf; 0001D567
vprop; 0000221D
vrtri; 000022B3
vscr; 0001D4CB
vzigzag; 0000299A
wcirc; 00000175
wedbar; 00002A5F
wedge; 00002227
wedgeq; 00002259
weierp; 00002118
wfr; 0001D534
wopf; 0001D568
wp; 00002118
wr; 00002240
wreath; 00002240
wscr; 0001D4CC
xcap; 000022C2
xcirc; 000025EF
xcup; 000022C3
xdtri; 000025BD
xfr; 0001D535
xhArr; 000027FA
xharr; 000027F7
xi; 000003BE
xlArr; 000027F8
xlarr; 000027F5
xmap; 000027FC
xnis; 000022FB
xodot; 00002A00
xopf; 0001D569
xoplus; 00002A01
xotime; 00002A02
xrArr; 000027F9
xrarr; 000027F6
xscr; 0001D4CD
xsqcup; 00002A06
xuplus; 00002A04
xutri; 000025B3
xvee; 000022C1
xwedge; 000022C0
yacute; 000000FD
yacy; 0000044F
ycirc; 00000177
ycy; 0000044B
yen; 000000A5
yfr; 0001D536
yicy; 00000457
yopf; 0001D56A
yscr; 0001D4CE
yucy; 0000044E
yuml; 000000FF
zacute; 0000017A
zcaron; 0000017E
zcy; 00000437
zdot; 0000017C
zeetrf; 00002128
zeta; 000003B6
zfr; 0001D537
zhcy; 00000436
zigrarr; 000021DD
zopf; 0001D56B
zscr; 0001D4CF
zwj; 0000200D
zwnj; 0000200C
AElig 000000C6
AMP 00000026
Aacute 000000C1
Acirc 000000C2
Agrave 000000C0
Aring 000000C5
Atilde 000000C3
Auml 000000C4
COPY 000000A9
Ccedil 000000C7
ETH 000000D0
Eacute 000000C9
Ecirc 000000CA
Egrave 000000C8
Euml 000000CB
GT 0000003E
Iacute 000000CD
Icirc 000000CE
Igrave 000000CC
Iuml 000000CF
LT 0000003C
Ntilde 000000D1
Oacute 000000D3
Ocirc 000000D4
Ograve 000000D2
Oslash 000000D8
Otilde 000000D5
Ouml 000000D6
QUOT 00000022
REG 000000AE
THORN 000000DE
Uacute 000000DA
Ucirc 000000DB
Ugrave 000000D9
Uuml 000000DC
Yacute 000000DD
aacute 000000E1
acirc 000000E2
acute 000000B4
aelig 000000E6
agrave 000000E0
amp 00000026
aring 000000E5
atilde 000000E3
auml 000000E4
brvbar 000000A6
ccedil 000000E7
cedil 000000B8
cent 000000A2
copy 000000A9
curren 000000A4
deg 000000B0
divide 000000F7
eacute 000000E9
ecirc 000000EA
egrave 000000E8
eth 000000F0
euml 000000EB
frac12 000000BD
frac14 000000BC
frac34 000000BE
gt 0000003E
iacute 000000ED
icirc 000000EE
iexcl 000000A1
igrave 000000EC
iquest 000000BF
iuml 000000EF
laquo 000000AB
lt 0000003C
macr 000000AF
micro 000000B5
middot 000000B7
nbsp 000000A0
not 000000AC
ntilde 000000F1
oacute 000000F3
ocirc 000000F4
ograve 000000F2
ordf 000000AA
ordm 000000BA
oslash 000000F8
otilde 000000F5
ouml 000000F6
para 000000B6
plusmn 000000B1
pound 000000A3
quot 00000022
raquo 000000BB
reg 000000AE
sect 000000A7
shy 000000AD
sup1 000000B9
sup2 000000B2
sup3 000000B3
szlig 000000DF
thorn 000000FE
times 000000D7
uacute 000000FA
ucirc 000000FB
ugrave 000000F9
uml 000000A8
uuml 000000FC
yacute 000000FD
yen 000000A5
yuml 000000FF
"""#
    private static let entity2Text = #"""
NotEqualTilde; 2242 0338
NotGreaterFullEqual; 2267 0338
NotGreaterGreater; 226B 0338
NotGreaterSlantEqual; 2A7E 0338
NotHumpDownHump; 224E 0338
NotHumpEqual; 224F 0338
NotLeftTriangleBar; 29CF 0338
NotLessLess; 226A 0338
NotLessSlantEqual; 2A7D 0338
NotNestedGreaterGreater; 2AA2 0338
NotNestedLessLess; 2AA1 0338
NotPrecedesEqual; 2AAF 0338
NotRightTriangleBar; 29D0 0338
NotSquareSubset; 228F 0338
NotSquareSuperset; 2290 0338
NotSubset; 2282 20D2
NotSucceedsEqual; 2AB0 0338
NotSucceedsTilde; 227F 0338
NotSuperset; 2283 20D2
ThickSpace; 205F 200A
acE; 223E 0333
bne; 003D 20E5
bnequiv; 2261 20E5
caps; 2229 FE00
cups; 222A FE00
fjlig; 0066 006A
gesl; 22DB FE00
gvertneqq; 2269 FE00
gvnE; 2269 FE00
lates; 2AAD FE00
lesg; 22DA FE00
lvertneqq; 2268 FE00
lvnE; 2268 FE00
nGg; 22D9 0338
nGtv; 226B 0338
nLl; 22D8 0338
nLtv; 226A 0338
nang; 2220 20D2
napE; 2A70 0338
napid; 224B 0338
nbump; 224E 0338
nbumpe; 224F 0338
ncongdot; 2A6D 0338
nedot; 2250 0338
nesim; 2242 0338
ngE; 2267 0338
ngeqq; 2267 0338
ngeqslant; 2A7E 0338
nges; 2A7E 0338
nlE; 2266 0338
nleqq; 2266 0338
nleqslant; 2A7D 0338
nles; 2A7D 0338
notinE; 22F9 0338
notindot; 22F5 0338
nparsl; 2AFD 20E5
npart; 2202 0338
npre; 2AAF 0338
npreceq; 2AAF 0338
nrarrc; 2933 0338
nrarrw; 219D 0338
nsce; 2AB0 0338
nsubE; 2AC5 0338
nsubset; 2282 20D2
nsubseteqq; 2AC5 0338
nsucceq; 2AB0 0338
nsupE; 2AC6 0338
nsupset; 2283 20D2
nsupseteqq; 2AC6 0338
nvap; 224D 20D2
nvge; 2265 20D2
nvgt; 003E 20D2
nvle; 2264 20D2
nvlt; 003C 20D2
nvltrie; 22B4 20D2
nvrtrie; 22B5 20D2
nvsim; 223C 20D2
race; 223D 0331
smtes; 2AAC FE00
sqcaps; 2293 FE00
sqcups; 2294 FE00
varsubsetneq; 228A FE00
varsubsetneqq; 2ACB FE00
varsupsetneq; 228B FE00
varsupsetneqq; 2ACC FE00
vnsub; 2282 20D2
vnsup; 2283 20D2
vsubnE; 2ACB FE00
vsubne; 228A FE00
vsupnE; 2ACC FE00
vsupne; 228B FE00
"""#

    private typealias PropFn = @convention(c) (Int32, Int32) -> Int32
    private typealias LowerFn = @convention(c) (Int32) -> Int32

    private struct Engine: @unchecked Sendable {
        let workAuth: NSRegularExpression
        let selfID: NSRegularExpression
        let consent: NSRegularExpression
        let essay: NSRegularExpression
        let tag: NSRegularExpression
        let entity: [String: UInt32]
        let entity2: [String: (UInt32, UInt32)]
        let script: PropFn
        let lower: LowerFn
    }

    private static let engine: Engine = {
        precondition(latin1Upper.utf8.count == 32 && latin1Lower.utf8.count == 32 && latinExtA.utf8.count == 128,
                     "latin fold tables drifted from fieldtriage/fold.go")
        precondition(cp1252.count == 32, "windows-1252 table drifted from html.unescapeEntity")
        let handle = dlopen("/usr/lib/libicucore.A.dylib", RTLD_NOW)
        guard let handle,
              let scriptSym = dlsym(handle, "u_getIntPropertyValue"),
              let lowerSym = dlsym(handle, "u_tolower") else {
            preconditionFailure("libicucore is missing u_getIntPropertyValue or u_tolower; the suggest skip cannot match fieldtriage.folds")
        }
        let entity = parseRows(entityText, width: 2)
        let entity2 = parsePairs(entity2Text)
        precondition(entity.count == 2138 && entity2.count == 91,
                     "html entity table drifted from Go's encoding/html")
        for key in ["amp;", "amp", "lt;", "lt", "gt;", "nbsp", "nbsp;", "shy", "shy;"] {
            precondition(entity[key] != nil, "html entity table missing \(key)")
        }
        return Engine(
            workAuth: re2(workAuthPattern),
            selfID: re2(selfIDPattern),
            consent: re2(consentPattern),
            essay: re2(essayPattern),
            tag: re2Literal("</?[A-Za-z!][^<>]*>"),
            entity: entity,
            entity2: entity2,
            script: unsafeBitCast(scriptSym, to: PropFn.self),
            lower: unsafeBitCast(lowerSym, to: LowerFn.self))
    }()

    /// True when suggest would not send this label to a model.
    static func applies(_ label: String) -> Bool {
        let g = guardCategory(label)
        if g.isEmpty { return false }
        // draft.NeverDrafted: a consent hit on an essay prompt is still drafted.
        if g == consent && matches(engine.essay, goLower(label)) { return false }
        return true
    }

    private static func guardCategory(_ label: String) -> String {
        let forms = folds(label)
        for form in forms where mixedScript(form) { return selfID }
        for form in forms {
            if matches(engine.workAuth, form) { return workAuth }
            if matches(engine.selfID, form) { return selfID }
            if matches(engine.consent, form) { return consent }
        }
        return ""
    }

    private static func re2(_ pattern: String) -> NSRegularExpression {
        let boundary = #"(?:(?<![A-Za-z0-9_])(?=[A-Za-z0-9_])|(?<=[A-Za-z0-9_])(?![A-Za-z0-9_]))"#
        let rewritten = pattern
            .replacingOccurrences(of: #"\w"#, with: "[A-Za-z0-9_]")
            .replacingOccurrences(of: #"\b"#, with: boundary)
        return re2Literal(rewritten)
    }

    private static func re2Literal(_ pattern: String) -> NSRegularExpression {
        do { return try NSRegularExpression(pattern: pattern, options: []) }
        catch { preconditionFailure("suggest-skip pattern did not compile: \(error)") }
    }

    private static func matches(_ re: NSRegularExpression, _ s: String) -> Bool {
        let range = NSRange(s.startIndex..., in: s)
        return re.firstMatch(in: s, options: [], range: range) != nil
    }

    private static func parseRows(_ text: String, width: Int) -> [String: UInt32] {
        var out: [String: UInt32] = [:]
        out.reserveCapacity(width == 2 ? 2300 : 8)
        for line in text.split(separator: "\n", omittingEmptySubsequences: true) {
            let parts = line.split(separator: " ")
            guard parts.count == width, let value = UInt32(parts[1], radix: 16), value != 0 else {
                preconditionFailure("bad html entity row")
            }
            out[String(parts[0])] = value
        }
        return out
    }

    private static func parsePairs(_ text: String) -> [String: (UInt32, UInt32)] {
        var out: [String: (UInt32, UInt32)] = [:]
        for line in text.split(separator: "\n", omittingEmptySubsequences: true) {
            let parts = line.split(separator: " ")
            guard parts.count == 3,
                  let a = UInt32(parts[1], radix: 16), a != 0,
                  let b = UInt32(parts[2], radix: 16) else {
                preconditionFailure("bad html entity2 row")
            }
            out[String(parts[0])] = (a, b)
        }
        return out
    }

    // MARK: folds (fieldtriage.folds)

    private static func folds(_ label: String) -> [String] {
        var label = label
        for _ in 0..<3 {
            let next = htmlUnescape(label)
            if next == label { break }
            label = next
        }
        var out: [String] = []
        out.reserveCapacity(4)
        for (invisibleIsSpace, joinTags) in [(false, false), (true, false), (false, true), (true, true)] {
            let s = fold(label, invisibleIsSpace: invisibleIsSpace, joinTags: joinTags)
            if !out.contains(s) { out.append(s) }
        }
        return out
    }

    private static func fold(_ label: String, invisibleIsSpace: Bool, joinTags: Bool) -> String {
        var b = ""
        b.reserveCapacity(label.utf8.count)
        for r in label.unicodeScalars {
            if isGoSpace(r) || r.value == 0x3000 {
                b.append(" ")
            } else if r.properties.generalCategory == .format || isBlankFiller(r.value) {
                if invisibleIsSpace { b.append(" ") }
            } else if isMark(r) {
                continue
            } else {
                b += baseLetter(r)
            }
        }
        if joinTags {
            let range = NSRange(b.startIndex..., in: b)
            b = engine.tag.stringByReplacingMatches(in: b, options: [], range: range, withTemplate: "")
        }
        return goFields(b).joined(separator: " ")
    }

    private static func isBlankFiller(_ v: UInt32) -> Bool {
        switch v {
        case 0x115F, 0x1160, 0x3164, 0xFFA0, 0x2800: return true
        default: return false
        }
    }

    private static func isMark(_ r: Unicode.Scalar) -> Bool {
        switch r.properties.generalCategory {
        case .nonspacingMark, .spacingMark, .enclosingMark: return true
        default: return false
        }
    }

    /// unicode.IsSpace, including the Latin-1 special cases Go hard-codes.
    private static func isGoSpace(_ r: Unicode.Scalar) -> Bool {
        let v = r.value
        if v <= 0xFF {
            switch v {
            case 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x20, 0x85, 0xA0: return true
            default: return false
            }
        }
        return r.properties.isWhitespace
    }

    private static func goFields(_ s: String) -> [String] {
        var parts: [String] = []
        var cur = ""
        for r in s.unicodeScalars {
            if isGoSpace(r) {
                if !cur.isEmpty { parts.append(cur); cur.removeAll(keepingCapacity: true) }
            } else {
                cur.unicodeScalars.append(r)
            }
        }
        if !cur.isEmpty { parts.append(cur) }
        return parts
    }

    private static func baseLetter(_ r: Unicode.Scalar) -> String {
        let v = r.value
        if v < 0x80 { return String(r) }
        if v >= 0xFF01 && v <= 0xFF5E { return String(Unicode.Scalar(v - 0xFEE0)!) }
        if v == 0xC6 { return "AE" }
        if v == 0xE6 { return "ae" }
        if v == 0xDE { return "TH" }
        if v == 0xFE { return "th" }
        if v == 0xDF { return "ss" }
        if v == 0x152 { return "OE" }
        if v == 0x153 { return "oe" }
        if v == 0x132 { return "IJ" }
        if v == 0x133 { return "ij" }
        if v >= 0xC0 && v <= 0xDF { return latinByte(latin1Upper, v - 0xC0, r) }
        if v >= 0xE0 && v <= 0xFF { return latinByte(latin1Lower, v - 0xE0, r) }
        if v >= 0x100 && v <= 0x17F {
            let byte = latinExtA.utf8[latinExtA.utf8.index(latinExtA.utf8.startIndex, offsetBy: Int(v - 0x100))]
            return String(Unicode.Scalar(byte))
        }
        if v == 0x251 { return "a" }
        if v == 0x261 { return "g" }
        if v >= 0x24B6 && v <= 0x24CF { return String(Unicode.Scalar(65 + v - 0x24B6)!) }
        if v >= 0x24D0 && v <= 0x24E9 { return String(Unicode.Scalar(97 + v - 0x24D0)!) }
        if v >= 0x1D400 && v <= 0x1D6A3 {
            let i = (v - 0x1D400) % 52
            if i < 26 { return String(Unicode.Scalar(65 + i)!) }
            return String(Unicode.Scalar(97 + i - 26)!)
        }
        if v >= 0x1D7CE && v <= 0x1D7FF { return String(Unicode.Scalar(48 + (v - 0x1D7CE) % 10)!) }
        return String(r)
    }

    private static func latinByte(_ table: String, _ offset: UInt32, _ r: Unicode.Scalar) -> String {
        let byte = table.utf8[table.utf8.index(table.utf8.startIndex, offsetBy: Int(offset))]
        if byte == UInt8(ascii: "-") { return String(r) }
        return String(Unicode.Scalar(byte))
    }

    // MARK: mixedScript (fieldtriage.mixedScript)

    private static func mixedScript(_ s: String) -> Bool {
        let ucharScript: Int32 = 0x100A
        let latinScript: Int32 = 25
        var latin = false
        var other = false
        for r in s.unicodeScalars {
            let script = engine.script(Int32(bitPattern: r.value), ucharScript)
            if script == latinScript {
                latin = true
            } else if isLookalike(script) {
                other = true
            } else if isGoLetter(r) || isGoDigit(r) {
                // a letter of an unrelated script: part of the word, neither kind
            } else {
                latin = false
                other = false
                continue
            }
            if latin && other { return true }
        }
        return false
    }

    private static func isLookalike(_ script: Int32) -> Bool {
        switch script {
        case 8, 14, 3, 6, 7, 12: return true // Cyrillic, Greek, Armenian, Cherokee, Coptic, Georgian
        default: return false
        }
    }

    private static func isGoLetter(_ r: Unicode.Scalar) -> Bool {
        switch r.properties.generalCategory {
        case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter, .otherLetter:
            return true
        default:
            return false
        }
    }

    private static func isGoDigit(_ r: Unicode.Scalar) -> Bool {
        r.properties.generalCategory == .decimalNumber
    }

    /// strings.ToLower, which is the simple lowercase mapping (one scalar out).
    private static func goLower(_ s: String) -> String {
        var out = ""
        out.reserveCapacity(s.utf8.count)
        for r in s.unicodeScalars {
            let mapped = engine.lower(Int32(bitPattern: r.value))
            if let lower = Unicode.Scalar(UInt32(bitPattern: mapped)) {
                out.unicodeScalars.append(lower)
            } else {
                out.unicodeScalars.append(r)
            }
        }
        return out
    }

    // MARK: html.UnescapeString (byte-oriented, three passes live in folds)

    private static func htmlUnescape(_ s: String) -> String {
        let b = Array(s.utf8)
        guard let first = b.firstIndex(of: UInt8(ascii: "&")) else { return s }
        var out: [UInt8] = []
        out.reserveCapacity(b.count)
        out.append(contentsOf: b[..<first])
        var src = first
        while src < b.count {
            if b[src] != UInt8(ascii: "&") {
                if let next = b[src...].firstIndex(of: UInt8(ascii: "&")) {
                    out.append(contentsOf: b[src..<next])
                    src = next
                } else {
                    out.append(contentsOf: b[src...])
                    break
                }
                continue
            }
            let (next, bytes) = unescapeEntity(b, src)
            if next <= src {
                out.append(b[src])
                src += 1
                continue
            }
            out.append(contentsOf: bytes)
            src = next
        }
        return String(decoding: out, as: UTF8.self)
    }

    private static func unescapeEntity(_ b: [UInt8], _ src: Int) -> (Int, [UInt8]) {
        let end = b.count
        let amp = UInt8(ascii: "&")
        let semi = UInt8(ascii: ";")
        if end - src <= 1 { return (src + 1, [b[src]]) }
        var i = 1
        if b[src + i] == UInt8(ascii: "#") {
            if end - src <= 3 { return (src + 1, [amp]) }
            i += 1
            var c = b[src + i]
            var hex = false
            if c == UInt8(ascii: "x") || c == UInt8(ascii: "X") {
                hex = true
                i += 1
            }
            var x: Int32 = 0
            while src + i < end {
                c = b[src + i]
                i += 1
                if hex {
                    if c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9") {
                        x = x &* 16 &+ (Int32(c) - 48)
                        continue
                    } else if c >= UInt8(ascii: "a") && c <= UInt8(ascii: "f") {
                        x = x &* 16 &+ (Int32(c) - 87)
                        continue
                    } else if c >= UInt8(ascii: "A") && c <= UInt8(ascii: "F") {
                        x = x &* 16 &+ (Int32(c) - 55)
                        continue
                    }
                } else if c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9") {
                    x = x &* 10 &+ (Int32(c) - 48)
                    continue
                }
                if c != semi { i -= 1 }
                break
            }
            if i <= 3 { return (src + 1, [amp]) }
            let bits = UInt32(bitPattern: x)
            let scalar: UInt32
            if bits >= 0x80 && bits <= 0x9F {
                scalar = cp1252[Int(bits - 0x80)]
            } else if x == 0 || (bits >= 0xD800 && bits <= 0xDFFF) || bits > 0x10FFFF {
                scalar = 0xFFFD
            } else {
                scalar = bits
            }
            return (src + i, utf8(scalar))
        }

        while src + i < end {
            let c = b[src + i]
            i += 1
            let alnum = (c >= UInt8(ascii: "a") && c <= UInt8(ascii: "z"))
                || (c >= UInt8(ascii: "A") && c <= UInt8(ascii: "Z"))
                || (c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9"))
            if alnum { continue }
            if c != semi { i -= 1 }
            break
        }
        let nameBytes = Array(b[(src + 1)..<(src + i)])
        if !nameBytes.isEmpty, let name = String(bytes: nameBytes, encoding: .utf8) {
            if let value = engine.entity[name], value != 0 {
                return (src + i, utf8(value))
            }
            if let pair = engine.entity2[name], pair.0 != 0 {
                return (src + i, utf8(pair.0) + utf8(pair.1))
            }
            var maxLen = nameBytes.count - 1
            if maxLen > 6 { maxLen = 6 }
            if maxLen > 1 {
                var j = maxLen
                while j > 1 {
                    if let prefix = String(bytes: nameBytes.prefix(j), encoding: .utf8),
                       let value = engine.entity[prefix], value != 0 {
                        return (src + j + 1, utf8(value))
                    }
                    j -= 1
                }
            }
        }
        return (src + i, Array(b[src..<(src + i)]))
    }

    private static func utf8(_ scalar: UInt32) -> [UInt8] {
        guard let s = Unicode.Scalar(scalar) else { return Array("\u{FFFD}".utf8) }
        return Array(s.utf8)
    }
}
