import Foundation

/// A bank refused as a whole: nothing is half-written.
public struct BankError: Error, Equatable, CustomStringConvertible {
    public let description: String
    public init(_ description: String) { self.description = description }
}

// A port of parseBank (extension/src/bank.ts), applied to every bank that
// reaches the store, whichever browser or the app sent it. Anything malformed
// is refused whole; a secret-classified answer is dropped and counted, so a
// hand-edited bank cannot smuggle in what capture refuses.

public enum BankValidator {
    public struct Result: Equatable {
        public let bank: Bank
        public let droppedSecret: Int
    }

    /// Validate a bank given as Foundation JSON objects (as JSONSerialization or Safari deliver it).
    public static func validate(_ raw: Any) throws -> Result {
        guard let r = raw as? [String: Any] else { throw BankError("bank is not an object") }
        guard let version = r["version"], isNumber(version), (version as? NSNumber)?.doubleValue == 1 else {
            throw BankError("bank version \(String(describing: r["version"] ?? "undefined")), want 1")
        }
        guard let answers = r["answers"] as? [String: Any] else { throw BankError("bank.answers is not an object") }
        if answers.count > Limits.maxAnswers {
            throw BankError("bank has \(answers.count) answers, max \(Limits.maxAnswers)")
        }

        var bank = Bank()
        if let s = r["settings"] as? [String: Any] {
            bank.settings.captureSelfId = isTrue(s["captureSelfId"])
            if let hosts = stringArray(s["disabledHosts"], cap: Limits.maxDisabledHosts) {
                bank.settings.disabledHosts = hosts
            }
        }

        var dropped = 0
        // Sorted so a refusal names the same answer every time.
        for key in answers.keys.sorted() {
            let quoted = String(String(reflecting: key).prefix(60))
            guard let a = answers[key] as? [String: Any], let k = a["key"] as? String, k == key,
                  key.utf16.count >= 2, key.utf16.count <= Limits.maxKeyChars else {
                throw BankError("answer \(quoted) is malformed")
            }
            guard let kindName = a["kind"] as? String, let kind = FieldKind(rawValue: kindName) else {
                throw BankError("answer \(key): unknown kind")
            }
            guard let value = stringArray(a["value"], cap: Limits.maxOptions),
                  let labels = stringArray(a["labels"], cap: Limits.maxLabels),
                  let signatures = stringArray(a["signatures"], cap: Limits.maxSignatures) else {
                throw BankError("answer \(key): value, labels or signatures malformed")
            }
            let sens = Classifier.classify(kind: kind, label: ([key] + labels).joined(separator: " "), name: "", autocomplete: "")
            if sens == .secret {
                dropped += 1
                continue
            }
            bank.answers[key] = Answer(
                key: key,
                labels: labels,
                kind: kind,
                value: value,
                sensitivity: sens,
                signatures: signatures,
                uses: count(a["uses"]),
                fills: count(a["fills"]),
                updatedAt: (a["updatedAt"] as? String).map { prefixUTF16($0, 40) } ?? "1970-01-01T00:00:00.000Z",
                review: isTrue(a["review"])
            )
        }

        let size = try bank.encoded().count
        if size > Limits.maxBankBytes {
            throw BankError("answer bank is \(size) bytes, over the \(Limits.maxBankBytes)-byte limit; delete some answers")
        }
        return Result(bank: bank, droppedSecret: dropped)
    }

    /// Validate a bank given as JSON text (an import file).
    public static func validate(json: Data) throws -> Result {
        if json.count > 8_000_000 { throw BankError("bank file is larger than 8 MB") }
        let raw: Any
        do { raw = try JSONSerialization.jsonObject(with: json, options: [.fragmentsAllowed]) } catch {
            throw BankError("bank is not valid JSON")
        }
        return try validate(raw)
    }

    /// Validate a bank built in Swift (an edit made in the app) by the same rules.
    public static func validate(_ bank: Bank) throws -> Result {
        try validate(bank.jsonObject())
    }

    // JSONSerialization returns booleans as NSNumber too; JavaScript's
    // `=== true` and `=== 1` tell them apart, so this does as well.
    static func isBool(_ x: Any?) -> Bool {
        guard let n = x as? NSNumber else { return false }
        return CFGetTypeID(n) == CFBooleanGetTypeID()
    }

    static func isNumber(_ x: Any?) -> Bool { x is NSNumber && !isBool(x) }

    static func isTrue(_ x: Any?) -> Bool { isBool(x) && (x as? NSNumber)?.boolValue == true }

    static func stringArray(_ x: Any?, cap: Int) -> [String]? {
        guard let arr = x as? [Any], arr.count <= cap else { return nil }
        var out: [String] = []
        out.reserveCapacity(arr.count)
        for item in arr {
            guard let s = item as? String, s.utf16.count <= Limits.maxValueChars else { return nil }
            out.append(s)
        }
        return out
    }

    /// `Number.isSafeInteger(x) && x >= 0 ? x : 0`.
    static func count(_ x: Any?) -> Int {
        guard isNumber(x), let d = (x as? NSNumber)?.doubleValue, d >= 0, d <= 9_007_199_254_740_991,
              d.rounded(.towardZero) == d else { return 0 }
        return Int(d)
    }
}
