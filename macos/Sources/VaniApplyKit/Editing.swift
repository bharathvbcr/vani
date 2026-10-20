import Foundation

// Edits the control app makes to the bank. Same rules as capture in the
// extension (bank.ts mergeCapture): secrets are refused, keys are normalized
// labels, long free text is filled only as a draft.

public enum EditError: Error, Equatable, CustomStringConvertible {
    case emptyQuestion
    case emptyAnswer
    case secret
    case tooLong
    case duplicate(String)
    case full

    public var description: String {
        switch self {
        case .emptyQuestion: "Enter the question as the form asks it."
        case .emptyAnswer: "Enter an answer."
        case .secret: "This looks like a password, government ID, card, bank or date-of-birth question. Vani Apply never stores those."
        case .tooLong: "An answer is limited to \(Limits.maxValueChars) characters and \(Limits.maxOptions) choices."
        case let .duplicate(key): "An answer for “\(key)” already exists; edit it instead."
        case .full: "The bank already holds \(Limits.maxAnswers) answers."
        }
    }
}

public enum Editing {
    /// Kinds a person can pick when adding an answer by hand.
    public static let manualKinds: [FieldKind] = [.text, .textarea, .email, .tel, .url, .number, .date, .select, .radio, .checkbox]

    /// Choice answers are one option per line; text answers are the whole text.
    public static func values(from text: String, kind: FieldKind) -> [String] {
        if kind.isChoice {
            return text.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        }
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? [] : [t]
    }

    public static func text(of answer: Answer) -> String {
        answer.value.joined(separator: "\n")
    }

    static func checked(_ value: [String]) throws -> [String] {
        if value.isEmpty { throw EditError.emptyAnswer }
        if value.count > Limits.maxOptions || value.contains(where: { $0.utf16.count > Limits.maxValueChars }) { throw EditError.tooLong }
        return value
    }

    static func review(_ kind: FieldKind, _ value: [String]) -> Bool {
        !kind.isChoice && value.joined(separator: " ").utf16.count > Limits.reviewChars
    }

    /// The answer that already holds this question, under its own key or a sibling
    /// wording's ("LinkedIn" for "LinkedIn profile URL"). Mirrors bank.ts ownerIndex:
    /// the concept key must be non-empty, the kinds both choice or both text, the
    /// sensitivity equal; among several the most used, then the newest, then the key.
    public static func owner(of question: String, kind: FieldKind, in bank: Bank) -> Answer? {
        let key = Classifier.normalizeLabel(question)
        if let direct = bank.answers[key] { return direct }
        let sens = Classifier.classify(kind: kind, label: question, name: "", autocomplete: "")
        let want = Classifier.conceptKey(label: question, autocomplete: "")
        if want.isEmpty { return nil }
        var best: Answer?
        for a in bank.answers.values where a.sensitivity == sens && a.kind.isChoice == kind.isChoice {
            guard ([a.key] + a.labels).contains(where: { Classifier.conceptKey(label: $0, autocomplete: "") == want }) else { continue }
            if let b = best, (a.uses, a.updatedAt, b.key) <= (b.uses, b.updatedAt, a.key) { continue }
            best = a
        }
        return best
    }

    /// A new answer typed into the app.
    public static func newAnswer(question: String, kind: FieldKind, text: String, in bank: Bank, now: Date = Date()) throws -> Answer {
        let key = Classifier.normalizeLabel(question)
        if key.utf16.count < 2 { throw EditError.emptyQuestion }
        let sens = Classifier.classify(kind: kind, label: question, name: "", autocomplete: "")
        if sens == .secret { throw EditError.secret }
        if let existing = owner(of: question, kind: kind, in: bank) { throw EditError.duplicate(existing.key) }
        if bank.answers.count >= Limits.maxAnswers { throw EditError.full }
        let value = try checked(values(from: text, kind: kind))
        return Answer(key: key, labels: [key], kind: kind, value: value, sensitivity: sens, signatures: [],
                      uses: 0, fills: 0, updatedAt: isoNow(now), review: review(kind, value))
    }

    /// An existing answer with a new value. The draft flag follows the length, as on capture.
    public static func edited(_ answer: Answer, text: String, now: Date = Date()) throws -> Answer {
        var a = answer
        a.value = try checked(values(from: text, kind: answer.kind))
        a.review = review(a.kind, a.value)
        a.updatedAt = isoNow(now)
        return a
    }

    /// A hostname typed into the disabled-sites list, or nil when it is not one.
    public static func hostname(_ input: String) -> String? {
        var s = input.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if let url = URL(string: s), let host = url.host, url.scheme != nil { s = host }
        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789.-")
        guard !s.isEmpty, s.utf8.count <= 253, s.contains("."), !s.hasPrefix("."), !s.hasSuffix("."),
              s.unicodeScalars.allSatisfy({ allowed.contains($0) }) else { return nil }
        return s
    }

    /// Save a model draft as the answer to `question`: a new answer, or the
    /// existing one's value replaced. Always marked for review, so the extension
    /// fills it yellow and never as a confident answer.
    public static func savingDraft(question: String, text: String, in bank: Bank, now: Date = Date()) throws -> Answer {
        var a: Answer
        if let existing = owner(of: question, kind: .textarea, in: bank) {
            a = try edited(existing, text: text, now: now)
        } else {
            a = try newAnswer(question: question, kind: .textarea, text: text, in: bank, now: now)
        }
        a.review = true
        return a
    }

    /// The bank as an export file, in the extension's export format.
    public static func exportData(_ bank: Bank) throws -> Data {
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        return try enc.encode(bank)
    }
}
