import Foundation

/// Two answers whose questions are worded alike, by key. `cos` is the score the
/// embedding model gave; `tier` is the model's own band for it, when it names one.
public struct SimilarPair: Equatable, Sendable {
    public var a: String
    public var b: String
    public var cos: Double
    public var tier: String?

    public init(a: String, b: String, cos: Double, tier: String? = nil) {
        self.a = a
        self.b = b
        self.cos = cos
        self.tier = tier
    }
}

/// What the similar-wording pass found. `unavailable` means the check did not run
/// (or its reply could not be trusted); it is never the same as "no duplicates".
public enum SimilarResult: Equatable, Sendable {
    case available([SimilarPair], model: String?)
    case unavailable(String)
}

/// Asks `vani answers similar --json` which of the bank's questions read alike.
/// Only keys and wordings go to the child on stdin; answer values never do.
public struct SimilarLabels: Sendable {
    public static let timeout: TimeInterval = 15
    static let maxOutputBytes = 8 << 20
    /// Pairs kept before the pass is judged too noisy to trust.
    static let maxPairs = 200_000

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, timeout: TimeInterval = SimilarLabels.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.timeout = timeout
    }

    struct Request {
        /// Distinct wordings, in a stable order.
        var texts: [String]
        /// The rows each wording belongs to.
        var owners: [[String]]

        var stdin: Data {
            let enc = JSONEncoder()
            enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
            return (try? enc.encode(Body(texts: texts))) ?? Data()
        }
        private struct Body: Encodable { let texts: [String] }
    }

    /// The keys and wordings of the rows a merge may touch (see Dedup.offered), at most 300 characters each.
    static func request(for bank: Bank) -> Request {
        var index: [String: Int] = [:]
        var req = Request(texts: [], owners: [])
        for a in bank.answers.values.filter({ Dedup.offered($0, settings: bank.settings) }).sorted(by: { $0.key < $1.key }) {
            for raw in [a.key] + a.labels {
                let t = prefixUTF16(raw, 300)
                if t.isEmpty { continue }
                if let i = index[t] {
                    if req.owners[i].last != a.key { req.owners[i].append(a.key) }
                } else {
                    index[t] = req.texts.count
                    req.texts.append(t)
                    req.owners.append([a.key])
                }
            }
        }
        return req
    }

    private struct Reply: Decodable {
        struct Pair: Decodable { let a: Int; let b: Int; let cos: Double; let tier: String? }
        let available: Bool
        let reason: String?
        let model: String?
        let pairs: [Pair]?
    }

    /// Blocks until the child exits or the timeout passes; call it off the main thread.
    public func find(in bank: Bank) -> SimilarResult {
        let req = Self.request(for: bank)
        if req.texts.count < 2 { return .available([], model: nil) }
        let reply: Reply
        do {
            reply = try VaniProcess.call(vaniBinary, ["answers", "similar", "--json"], configPath: configPath, stateDir: stateDir,
                                         input: req.stdin, timeout: timeout, maxOutputBytes: Self.maxOutputBytes,
                                         tooOld: "this vani predates similar-wording grouping")
        } catch {
            return .unavailable(String(describing: error))
        }
        guard reply.available else {
            return .unavailable(reply.reason.flatMap { $0.isEmpty ? nil : String($0.prefix(200)) } ?? "vani reported no reason")
        }
        guard let raw = reply.pairs else { return .unavailable("vani's reply had no pairs") }
        var out: [SimilarPair] = []
        var seen = Set<String>()
        for p in raw {
            guard p.a >= 0, p.b >= 0, p.a < req.texts.count, p.b < req.texts.count, p.a != p.b, p.cos.isFinite else {
                return .unavailable("vani's reply named a wording that was not sent")
            }
            for ka in req.owners[p.a] {
                for kb in req.owners[p.b] where ka != kb {
                    let (x, y) = ka < kb ? (ka, kb) : (kb, ka)
                    guard seen.insert("\(x)\u{1F}\(y)").inserted else { continue }
                    if out.count >= Self.maxPairs { return .unavailable("vani returned more than \(Self.maxPairs) similar pairs") }
                    out.append(SimilarPair(a: x, b: y, cos: p.cos, tier: p.tier))
                }
            }
        }
        return .available(out, model: reply.model.flatMap { $0.isEmpty ? nil : String($0.prefix(100)) })
    }
}
