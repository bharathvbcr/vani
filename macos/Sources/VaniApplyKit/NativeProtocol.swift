import Foundation

// The request/reply protocol the browser extensions speak, identical for the
// Chrome native host (length-prefixed JSON on stdio) and the Safari native
// extension (Foundation objects through SFExtensionMessageKey):
//
//   {"op":"ping"}                         -> {"ok":true,"protocol":1,"rev":N}
//   {"op":"get"}                          -> {"rev":N,"bank":{...}}
//   {"op":"put","baseRev":N,"bank":{...}} -> {"rev":N+1,"droppedSecret":K}
//                                          | {"conflict":true,"rev":M,"bank":{...}}
//   {"op":"log","entry":{"event":...,"host"?:...,"counts"?:{...}}} -> {"ok":true}
//   {"op":"draft","request":{"question":...,"job":{...},"max_words"?:N}}
//                                         -> {"draft","model","evidence_items"} | {"error","code"}
//   anything refused                      -> {"error":"..."}
//
// Merging stays in the extension (one implementation); this side is a
// validated compare-and-swap store. Drafting runs `vani draft` and is offered
// only where a DraftRunner is passed (the Chrome host); the sandboxed Safari
// extension cannot start it and answers code "unsupported".

public enum NativeProtocol {
    public static let version = 1

    public static func handle(_ request: Any, store: BankStore, source: Source, drafter: DraftRunner? = nil) -> [String: Any] {
        do {
            return try dispatch(request, store: store, source: source, drafter: drafter)
        } catch {
            return ["error": String(describing: error)]
        }
    }

    private static func dispatch(_ request: Any, store: BankStore, source: Source, drafter: DraftRunner?) throws -> [String: Any] {
        guard let req = request as? [String: Any], let op = req["op"] as? String else {
            throw BankError("request is not an object with an op")
        }
        switch op {
        case "ping":
            return ["ok": true, "protocol": version, "rev": try store.read().rev]
        case "get":
            let snap = try store.read()
            return ["rev": snap.rev, "bank": try snap.bank.jsonObject()]
        case "put":
            guard let baseRev = req["baseRev"], BankValidator.isNumber(baseRev),
                  let base = (baseRev as? NSNumber)?.intValue, base >= 0 else {
                throw BankError("put needs a non-negative baseRev")
            }
            guard let bank = req["bank"] else { throw BankError("put needs a bank") }
            switch try store.put(baseRev: base, bank: bank) {
            case let .stored(rev, dropped):
                return ["rev": rev, "droppedSecret": dropped]
            case let .conflict(snap):
                return ["conflict": true, "rev": snap.rev, "bank": try snap.bank.jsonObject()]
            }
        case "log":
            try store.log(try activityEntry(req["entry"], source: source))
            return ["ok": true]
        case "draft":
            guard let drafter else {
                return ["error": "drafting is not available here; use Draft in the Vani Apply app", "code": "unsupported"]
            }
            let draftRequest: DraftRequest
            do { draftRequest = try DraftRunner.request(from: req["request"]) } catch {
                return ["error": String(describing: error), "code": "bad_request"]
            }
            let reply = drafter.run(draftRequest)
            // Only an event and a count reach the log: never the question or the draft.
            try? store.log(ActivityEntry(source: source, event: .draft, counts: [reply.error == nil ? "drafted" : "failed": 1]))
            return reply.asJSONObject
        default:
            throw BankError("unknown op \(String(op.prefix(40)))")
        }
    }

    private static let hostChars = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789.-")
    private static let countNameChars = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

    /// Accept only an event name, a hostname and a few named counts: no free text can reach the log.
    static func activityEntry(_ raw: Any?, source: Source) throws -> ActivityEntry {
        guard let e = raw as? [String: Any], let name = e["event"] as? String,
              let event = ActivityEntry.Event(rawValue: name) else {
            throw BankError("log entry needs a known event")
        }
        var host: String?
        if let h = e["host"] {
            guard let s = h as? String, !s.isEmpty, s.utf8.count <= 253,
                  s.lowercased().unicodeScalars.allSatisfy({ hostChars.contains($0) }) else {
                throw BankError("log entry host is not a hostname")
            }
            host = s.lowercased()
        }
        var counts: [String: Int]?
        if let c = e["counts"] {
            guard let dict = c as? [String: Any], dict.count <= 10 else { throw BankError("log entry counts malformed") }
            var out: [String: Int] = [:]
            for (k, v) in dict {
                guard !k.isEmpty, k.count <= 32, k.unicodeScalars.allSatisfy({ countNameChars.contains($0) }),
                      BankValidator.isNumber(v), let d = (v as? NSNumber)?.doubleValue,
                      d >= 0, d <= 1_000_000, d.rounded(.towardZero) == d else {
                    throw BankError("log entry counts malformed")
                }
                out[k] = Int(d)
            }
            counts = out
        }
        return ActivityEntry(source: source, event: event, host: host, counts: counts)
    }
}
