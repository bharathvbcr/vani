import Foundation

// A port of conceptKey (extension/src/bank.ts) with what it reads: intentOf
// (intents.ts), tokens and stem (normalize.ts) and autocompleteKey. Held to
// extension/test/vectors/concept.json, which bun test replays too. As in
// Classifier.swift the regexes keep JavaScript's semantics: `\b`, `\w` and `\d`
// are ASCII there, so they are rewritten to ASCII classes before compiling.

extension Classifier {
    private static func js(_ pattern: String) -> NSRegularExpression {
        let ascii = "[A-Za-z0-9_]"
        let boundary = "(?:(?<=\(ascii))(?!\(ascii))|(?<!\(ascii))(?=\(ascii)))"
        let p = pattern
            .replacingOccurrences(of: #"\b"#, with: boundary)
            .replacingOccurrences(of: #"\w"#, with: ascii)
            .replacingOccurrences(of: #"\d"#, with: "[0-9]")
        do { return try NSRegularExpression(pattern: p) } catch {
            preconditionFailure("invalid built-in pattern \(pattern): \(error)")
        }
    }

    private static func hit(_ re: NSRegularExpression, _ s: String) -> Bool {
        re.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) != nil
    }

    // MARK: intents (extension/src/intents.ts; only the label and the patterns matter here)

    private static let intents: [(key: String, patterns: [NSRegularExpression])] = [
        ("Authorized to work", [
            #"\b(authori[sz]ed|eligible|permitted|legally able|entitled) to work\b"#,
            #"\bwork authori[sz]ation\b"#,
            #"\bright to work\b"#,
        ]),
        ("Visa sponsorship", [#"\bsponsor(ship|ed|ing)?\b"#]),
        ("Open to relocation", [
            #"\b(open|willing|able|consider|considering|happy)\b.*\breloca(te|tion|ting)\b"#,
        ]),
        ("Open to working in person", [
            #"\b(open|willing|able|comfortable|happy|commit|agree)\b.*\b(in person|onsite|on site|in office|in the office|in (one of )?(our|the|an|a) offices?|from (our|the) [a-z ]*office|hybrid)\b"#,
            #"\b(in person|onsite|on site|hybrid)\b.*\b(open|willing|able|comfortable)\b"#,
        ]),
        ("Where you'll work from", [
            #"\baddress from which you\b"#,
            #"\bwhere (youll|you will|will you) (be )?(work|working|based)( from)?\b"#,
            #"\bwhere (are|do) you (currently )?(located|based|live|living)\b"#,
            #"\b(your|current) (current )?location\b"#,
        ]),
        ("Interviewed here before", [
            #"\b(ever|previously|before|past)\b.*\binterviewed (at|with|for|here)\b"#,
            #"\binterviewed (at|with|for|here)\b.*\b(before|previously|in the past)\b"#,
        ]),
        ("Applied here before", [
            #"\b(ever|previously|before|past)\b.*\bapplied (to|at|with|here)\b"#,
            #"\bapplied (to|at|with|here)\b.*\b(before|previously|in the past)\b"#,
        ]),
        ("Earliest start date", [
            #"\b(earliest|soonest)\b.*\bstart\b"#,
            #"\b(when|how soon)\b.*\b(can|could|would|able to)\b.*\bstart\b"#,
            #"\b(desired|preferred|expected|available|availability|potential) start\b"#,
            #"\bavailab\w*\b.*\bto start\b"#,
            #"\bnotice period\b"#,
        ]),
        ("Deadlines or timeline considerations", [
            #"\bdeadlines?\b"#,
            #"\btimeline\b"#,
            #"\b(other|competing|pending|outstanding) offers?\b"#,
        ]),
        ("How did you hear about us", [
            #"\bhow did you (hear|learn|find out|come across|discover)\b"#,
            #"\bwhere did you (hear|learn|find|see)\b"#,
        ]),
        ("Salary expectations", [
            #"\b(salary|compensation|pay)\b.*\b(expectations?|requirements?|range|desired|expected)\b"#,
            #"\b(expected|desired|target) (salary|compensation|pay|base)\b"#,
        ]),
        ("AI policy for applications", [
            #"\bai\b.*\bpolic(y|ies)\b"#,
            #"\bpolic(y|ies)\b.*\bai\b"#,
            #"\b(use of|using) (ai|artificial intelligence)\b"#,
        ]),
        ("Arbitration agreement", [#"\barbitrat(e|ion|or)\b"#]),
        ("Gender", [#"\bgender\b"#]),
        ("Hispanic or Latino", [#"\bhispanic\b"#, #"\blatin[oax]\b"#]),
        ("Race", [#"\brace\b"#, #"\bracial\b"#]),
        ("Veteran status", [#"\bveteran\b"#]),
        ("Disability status", [#"\bdisabilit(y|ies)\b"#, #"\bdisabled\b"#]),
    ].map { (normalizeLabel($0.0), $0.1.map(js)) }

    private static let sectionEntry = js(#"\bvani section \d+$"#)

    /// The intent a question asks, or "" when it asks none, or reads as more than one.
    static func intentOf(_ question: String) -> String {
        let q = normalizeLabel(question)
        if q.isEmpty || hit(sectionEntry, q) { return "" }
        var found = ""
        for i in intents where i.patterns.contains(where: { hit($0, q) }) {
            if !found.isEmpty { return "" }
            found = i.key
        }
        return found
    }

    // MARK: tokens (extension/src/normalize.ts)

    private static let stopwords: Set<String> = [
        "a", "an", "the", "your", "you", "yours", "do", "does", "are", "is", "be", "of", "to", "in", "on",
        "for", "and", "or", "please", "enter", "provide", "what", "which", "if", "any", "this", "that",
        "with", "at", "by", "as", "me", "my", "we", "our", "us", "will", "would", "can", "have", "has",
        "url", "link", "profile", "page", "number", "handle", "here",
    ]

    private static let phrases: [(NSRegularExpression, String)] = [
        (js(#"\b(the )?united states( of america)?\b|\bu s a\b|\busa\b|\bthe us\b|\bu s\b"#), " usa "),
        (js(#"\be mail\b"#), " email "),
        (js(#"\bmobile\b|\bcell( phone)?\b"#), " phone "),
    ]

    /// A light plural/verb-ending stem, on UTF-16 units like the JavaScript it mirrors.
    private static func stem(_ w: String) -> String {
        let u = Array(w.utf16)
        if u.count <= 3 { return w }
        for suf in ["ization", "ation", "ship", "ing", "ed", "es", "s"] {
            let s = Array(suf.utf16)
            if u.count >= s.count, Array(u.suffix(s.count)) == s, u.count - s.count >= 3 {
                return String(decoding: u.prefix(u.count - s.count), as: UTF16.self)
            }
        }
        return w
    }

    /// Content words of a label: normalized, phrase-folded, stopwords dropped, stemmed.
    static func tokens(_ s: String) -> Set<String> {
        var n = normalizeLabel(s)
        for (re, to) in phrases {
            n = re.stringByReplacingMatches(in: n, range: NSRange(n.startIndex..., in: n), withTemplate: to)
        }
        var out = Set<String>()
        for w in n.split(separator: " ", omittingEmptySubsequences: true) where !stopwords.contains(String(w)) {
            out.insert(stem(String(w)))
        }
        return out
    }

    // MARK: autocomplete

    private static let autocompleteKeys: [String: String] = [
        "given-name": "first name", "family-name": "last name", "additional-name": "middle name",
        "name": "full name", "email": "email", "tel": "phone", "tel-national": "phone",
        "street-address": "street address", "address-line1": "street address", "address-line2": "address line 2",
        "address-level2": "city", "address-level1": "state", "postal-code": "postal code",
        "country": "country", "country-name": "country", "organization": "current company",
        "organization-title": "current title", "url": "website",
    ]

    /// The canonical key an autocomplete attribute implies, or "".
    static func autocompleteKey(_ autocomplete: String) -> String {
        let parts = autocomplete.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
            .split(whereSeparator: { $0.isWhitespace })
        return autocompleteKeys[parts.last.map(String.init) ?? ""] ?? ""
    }

    // MARK: concept

    /// The meaning of a question, independent of its wording: the intent id when
    /// the label reads as exactly one intent, else the autocomplete meaning, else
    /// its sorted unique content tokens. "" when nothing identifies it.
    public static func conceptKey(label: String, autocomplete: String) -> String {
        let intent = intentOf(label)
        if !intent.isEmpty { return intent }
        let ac = autocompleteKey(autocomplete)
        if !ac.isEmpty { return ac }
        return tokens(label).sorted { Array($0.utf16).lexicographicallyPrecedes(Array($1.utf16)) }.joined(separator: " ")
    }
}
