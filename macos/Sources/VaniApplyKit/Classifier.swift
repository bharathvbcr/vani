import Foundation

// A port of extension/src/classify.ts and normalizeLabel from normalize.ts.
// Both are held to extension/test/vectors/classify.json. The regexes keep
// JavaScript's semantics on purpose: JS `\b` (without the u flag) is an ASCII
// word boundary, so it is spelled out as ASCII lookarounds here; ICU's `\b`
// would treat "ssné" differently from the extension.

public enum Classifier {
    private static func regex(_ pattern: String, _ options: NSRegularExpression.Options = []) -> NSRegularExpression {
        do { return try NSRegularExpression(pattern: pattern, options: options) } catch {
            preconditionFailure("invalid built-in pattern \(pattern): \(error)")
        }
    }

    private static let asciiBefore = "(?<![A-Za-z0-9_])"
    private static let asciiAfter = "(?![A-Za-z0-9_])"

    private static let requiredMarks = regex(#"\(\s*(required|optional)\s*\)|\*|†"#, [.caseInsensitive])
    private static let apostrophes = regex("[’']")
    private static let nonWord = regex(#"[^\p{L}\p{N}]+"#)
    private static let spaces = regex(#"\s+"#)

    private static let secretWords = [
        "ssn", "social security", "social insurance", "national insurance", "passport", "tax id", "tin",
        "itin", "ein", "drivers? licen[cs]e", "driving licen[cs]e", "bank", "routing", "iban", "swift",
        "account number", "credit card", "debit card", "card number", "cvv", "cvc", "expiry", "expiration",
        "date of birth", "birth date", "birthdate", "dob", "password", "passcode", "pin", "captcha",
        "security question", "mother s maiden", "maiden name", "one time code", "verification code",
    ]
    private static let selfIDWords = [
        "gender", "sex", "race", "racial", "ethnicity", "ethnic", "hispanic", "latino", "latina", "latinx",
        "veteran", "military status", "disability", "disabled", "sexual orientation", "transgender",
        "pronouns?", "lgbtq", "self identify", "self identification", "eeo", "voluntary",
    ]
    private static let secretLabel = regex(asciiBefore + "(" + secretWords.joined(separator: "|") + ")" + asciiAfter)
    private static let selfIDLabel = regex(asciiBefore + "(" + selfIDWords.joined(separator: "|") + ")" + asciiAfter)
    private static let secretAutocomplete = regex(asciiBefore + "(cc-|current-password|new-password|one-time-code|bday)")
    private static let nameSeparators = regex(#"[_\-\[\]]+"#)

    private static func replace(_ re: NSRegularExpression, in s: String, with t: String) -> String {
        re.stringByReplacingMatches(in: s, range: NSRange(s.startIndex..., in: s), withTemplate: t)
    }

    private static func matches(_ re: NSRegularExpression, _ s: String) -> Bool {
        re.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) != nil
    }

    /// Lowercase, drop required/optional markers and punctuation, collapse spaces, cap at 200 UTF-16 units.
    public static func normalizeLabel(_ s: String) -> String {
        var t = s.precomposedStringWithCompatibilityMapping
        t = replace(requiredMarks, in: t, with: " ")
        t = t.lowercased()
        t = replace(apostrophes, in: t, with: "")
        t = replace(nonWord, in: t, with: " ")
        t = t.trimmingCharacters(in: .whitespacesAndNewlines)
        t = replace(spaces, in: t, with: " ")
        return prefixUTF16(t, Limits.maxKeyChars)
    }

    public static func classify(kind: FieldKind, label: String, name: String, autocomplete: String) -> Sensitivity {
        if kind == .password { return .secret }
        if autocomplete.hasPrefix("cc-") || matches(secretAutocomplete, autocomplete) { return .secret }
        let text = normalizeLabel(label + " " + replace(nameSeparators, in: name, with: " "))
        if matches(secretLabel, text) { return .secret }
        if matches(selfIDLabel, text) { return .selfID }
        return .standard
    }
}

/// JavaScript's `s.slice(0, n)`: the first n UTF-16 code units.
func prefixUTF16(_ s: String, _ n: Int) -> String {
    let u = s.utf16
    if u.count <= n { return s }
    return String(decoding: Array(u.prefix(n)), as: UTF16.self)
}
