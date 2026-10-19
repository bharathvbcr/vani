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
//   {"op":"resumePut","key":pageKey,"name":"x.pdf","pdf":base64}
//                                         -> {"ok":true,"key","name","bytes","sha256","pickedAt"} | {"error","code"}
//   {"op":"resumeGet","key":pageKey}      -> {"found":false} | {"found":true,"name","bytes","sha256","pickedAt","pdf":base64}
//                                          | {"error","code"}
//   {"op":"resumeInfo","key":pageKey}     -> resumeGet without "pdf"
//   {"op":"resumeDelete","key":pageKey}   -> {"ok":true,"removed":bool} | {"error","code"}
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
//
// The resume ops (ResumeStore.swift) bind one PDF to a job page (its pageKey, the
// same reduced URL jobFind compares) and hand it to the tab being filled. Both
// browsers run them in-process: the store is a folder beside the bank in the App
// Group container. The log gets an event and a count, never the file name or the page.

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
            if reply["job"] != nil {
                let event: ActivityEntry.Event = kind == JobKind.resume.rawValue ? .resume : .draft
                try? store.log(ActivityEntry(source: source, event: event, counts: [kind == JobKind.resume.rawValue ? "building" : "started": 1]))
            }
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
        case "resumePut", "resumeGet", "resumeInfo", "resumeDelete":
            return resumeOp(op, req, store: store, source: source)
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

    /// base64 of ResumeStore.maxBytes plus padding slack: a longer string is refused before it is decoded.
    private static let maxResumeBase64 = (ResumeStore.maxBytes + 2) / 3 * 4 + 8

    private static func resumeOp(_ op: String, _ req: [String: Any], store: BankStore, source: Source) -> [String: Any] {
        let resumes = ResumeStore(directory: store.directory.appendingPathComponent("resumes", isDirectory: true))
        guard let key = JobLinks.checkedURL(req["key"]) else {
            return ["error": "\(op) needs the job page's http(s) key", "code": "bad_request"]
        }
        func described(_ e: ResumeStore.Entry) -> [String: Any] {
            ["found": true, "name": e.name, "bytes": e.bytes, "sha256": e.sha256, "pickedAt": e.pickedAt]
        }
        do {
            switch op {
            case "resumePut":
                guard let name = ResumeStore.checkedName(req["name"]) else {
                    return ["error": "resumePut needs a file name ending in .pdf, at most \(ResumeStore.maxNameChars) characters", "code": "bad_request"]
                }
                guard let b64 = req["pdf"] as? String else { return ["error": "resumePut needs the PDF", "code": "bad_request"] }
                if b64.utf8.count > maxResumeBase64 {
                    return ["error": "the resume is over \(ResumeStore.maxBytes / 1024) KB", "code": "too_large"]
                }
                guard let data = Data(base64Encoded: b64) else { return ["error": "the resume arrived damaged", "code": "bad_request"] }
                let e = try resumes.put(key: key, name: name, data: data)
                try? store.log(ActivityEntry(source: source, event: .resume, counts: ["chosen": 1]))
                return ["ok": true, "key": key, "name": e.name, "bytes": e.bytes, "sha256": e.sha256, "pickedAt": e.pickedAt]
            case "resumeGet":
                guard let (e, data) = try resumes.get(key: key) else { return ["found": false] }
                var out = described(e)
                out["pdf"] = data.base64EncodedString()
                return out
            case "resumeInfo":
                guard let e = try resumes.info(key: key) else { return ["found": false] }
                return described(e)
            default: // resumeDelete
                let removed = try resumes.delete(key: key)
                if removed { try? store.log(ActivityEntry(source: source, event: .resume, counts: ["removed": 1])) }
                return ["ok": true, "removed": removed]
            }
        } catch let f as ResumeStore.Failure {
            return ["error": f.message, "code": f.code]
        } catch {
            return ["error": String(describing: error), "code": "io"]
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
