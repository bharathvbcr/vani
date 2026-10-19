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
//   {"op":"jobStart","kind":"draft"|"suggest","request":{...}}
//                                         -> {"job":id} | {"error","code"}
//   {"op":"jobResult","job":id}           -> {"pending":true} | {"done":true,"reply":{...}} | {"error","code"}
//   {"op":"jobFind","url":"https://..."}  -> {"found":bool,"url","matches":N,"ambiguous"?,"job"?:{key,company,title,status,mark?,closed?,matchedOn}}
//                                          | {"error","code"}
//   {"op":"jobMark","key":"gh:acme:123"}  -> {"ok":true,"key","mark":"applied"} | {"error","code"}
//   anything refused                      -> {"error":"..."}
//
// Merging stays in the extension (one implementation); this side is a
// validated compare-and-swap store. The synchronous "draft" op runs
// `vani draft` and is offered only where a DraftRunner is passed (the Chrome
// host). Jobs run in the Vani Apply agent (Agent.swift), which both browsers
// reach over XPC: that is how the sandboxed Safari extension gets drafts and
// suggestions, since it cannot start `vani` itself.
//
// "jobFind" and "jobMark" (JobApplied.swift) connect the extension to the Jobs
// tab: find reads which tracked posting a page is, mark records Applied after
// the user's click. They run `vani jobs`: in the Chrome host through the JobsBackend
// it passes, and for Safari (whose appex cannot start `vani`) through the agent's
// XPC calls, since XPCAgentClient is a JobsBackend. With neither they answer code
// "unsupported".

public enum NativeProtocol {
    public static let version = 1

    public static func handle(_ request: Any, store: BankStore, source: Source, drafter: DraftRunner? = nil,
                              agent: AgentClient? = nil, jobs: JobsBackend? = nil) -> [String: Any] {
        do {
            return try dispatch(request, store: store, source: source, drafter: drafter, agent: agent, jobs: jobs)
        } catch {
            return ["error": String(describing: error)]
        }
    }

    private static func dispatch(_ request: Any, store: BankStore, source: Source, drafter: DraftRunner?,
                                 agent: AgentClient?, jobs: JobsBackend?) throws -> [String: Any] {
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
        case "jobStart":
            guard let agent else { return ["error": "jobs are not available here", "code": "unsupported"] }
            guard let kind = req["kind"] as? String, JobKind(rawValue: kind) != nil else {
                return ["error": "jobStart needs a known kind", "code": "bad_request"]
            }
            guard let body = req["request"], JSONSerialization.isValidJSONObject(body),
                  let data = try? JSONSerialization.data(withJSONObject: body) else {
                return ["error": "jobStart needs a request object", "code": "bad_request"]
            }
            let reply = agent.start(kind: kind, request: data)
            // Only an event and a count reach the log: never a label, a posting or a draft.
            if reply["job"] != nil { try? store.log(ActivityEntry(source: source, event: .draft, counts: ["started": 1])) }
            return reply
        case "jobResult":
            guard let agent else { return ["error": "jobs are not available here", "code": "unsupported"] }
            guard let job = req["job"] as? String, !job.isEmpty, job.utf8.count <= 64 else {
                return ["error": "jobResult needs a job id", "code": "bad_request"]
            }
            return agent.result(job: job)
        case "jobFind":
            guard let jobs = jobs ?? (agent as? JobsBackend) else { return ["error": "jobs are not available here", "code": "unsupported"] }
            guard let url = JobLinks.checkedURL(req["url"]) else {
                return ["error": "jobFind needs an http(s) url of at most \(JobLinks.maxURLBytes) bytes", "code": "bad_request"]
            }
            // Nothing about the page reaches the log: not its URL, not what it matched.
            do { return try jobs.find(url: url).asJSONObject } catch {
                return ["error": String(describing: error), "code": "jobs"]
            }
        case "jobMark":
            guard let jobs = jobs ?? (agent as? JobsBackend) else { return ["error": "jobs are not available here", "code": "unsupported"] }
            guard let key = JobLinks.checkedKey(req["key"]) else {
                return ["error": "jobMark needs a posting key", "code": "bad_request"]
            }
            // Applied is the only mark a browser can set. Clearing or hiding a job stays in the app.
            do { try jobs.markApplied(key: key) } catch {
                return ["error": String(describing: error), "code": "jobs"]
            }
            return ["ok": true, "key": key, "mark": JobMark.applied.rawValue]
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
