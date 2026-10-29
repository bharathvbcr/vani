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
//   {"op":"draft","request":{"question":...,"job":{...},"max_words"?:N,"attempt"?:N}}
//                                         -> {"draft","model","evidence_items"} | {"error","code"}
//   {"op":"jobStart","kind":"draft"|"suggest","request":{...}}
//                                         -> {"job":id} | {"error","code"}
//   {"op":"jobResult","job":id}           -> {"pending":true} | {"done":true,"reply":{...}} | {"error","code"}
//   {"op":"jobFind","url":"https://..."}  -> {"found":bool,"url","matches":N,"ambiguous"?,"job"?:{key,company,title,status,mark?,closed?,matchedOn,row?,repeatApply?,applyFormNote?}}
//                                          | {"error","code"}
//   {"op":"jobMark","key":"gh:acme:123"}  -> {"ok":true,"key","mark":"applied"} | {"error","code"}
//   {"op":"jobsIngest","page":{"url","title"?},"save_search"?:bool,"jobs":[{title,url,company?,location?,snippet?,posted?}]}
//                                         -> `vani jobs ingest --json` output unchanged: {queued,duplicates,rejected,search,analysis}
//                                          | {"error","code"}   (code: bad_request | unsupported | jobs;
//                                            bounds in JobIngest, contract in docs/EXTENSION_INGEST.md)
//   {"op":"resumePut","key":pageKey,"name":"x.pdf","pdf":base64}
//                                         -> {"ok":true,"key","name","bytes","sha256","pickedAt"} | {"error","code"}
//   {"op":"resumeGet","key":pageKey}      -> {"found":false} | {"found":true,"name","bytes","sha256","pickedAt","pdf":base64}
//                                          | {"error","code"}
//   {"op":"resumeInfo","key":pageKey}     -> resumeGet without "pdf"
//   {"op":"resumeDelete","key":pageKey}   -> {"ok":true,"removed":bool} | {"error","code"}
//   {"op":"resumeOpen","key":pageKey}     -> {"ok":true,"name","bytes"} | {"found":false} | {"error","code"}
//                                          the PDF is opened in Preview; the reply has no "pdf" and nothing is logged
//   {"op":"resumeOpenData","name","pdf"}  -> {"ok":true,"name","bytes"} | {"error","code"}
//                                          a PDF the page already holds (just built); not stored, not logged
//   {"op":"sameQuestion","a":"<label>","b":"<label>"}   (each 1..300 characters after trimming)
//                                         -> {"reading":"answered","value":"same"|"different"}
//                                          | {"reading":"abstained"|"refused"|"unavailable"|"failed","detail":"<closed code>"}
//                                          | {"error","code":"bad_request"}   (SameQuestion.swift; on-device model, labels only, ≤ 6 s,
//                                            nothing logged; Safari reaches it through the agent, Chrome starts vani-fm itself)
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
// The resume ops (ResumeStore.swift) bind one PDF to a job page (its posting key,
// the same reduced URL the extension stores under) and hand it to the tab being
// filled. resumeOpen and resumeOpenData also write a temporary copy and hand that
// URL to `openResume`, which the host and the Safari appex implement with
// NSWorkspace; the kit itself stays free of AppKit. With no opener those two ops
// answer "unsupported" and write nothing. The log gets an event and a count for a
// put or a delete, never for a read or an open, and never the file name or the page.

public enum NativeProtocol {
    public static let version = 1

    public static func handle(_ request: Any, store: BankStore, source: Source, drafter: DraftRunner? = nil,
                              agent: AgentClient? = nil, jobs: JobsBackend? = nil,
                              openResume: (@Sendable (URL) -> Bool)? = nil,
                              sameQuestion: SameQuestionBackend? = nil) -> [String: Any] {
        do {
            return try dispatch(request, store: store, source: source, drafter: drafter, agent: agent, jobs: jobs,
                                openResume: openResume, sameQuestion: sameQuestion)
        } catch {
            return ["error": String(describing: error)]
        }
    }

    private static func dispatch(_ request: Any, store: BankStore, source: Source, drafter: DraftRunner?,
                                 agent: AgentClient?, jobs: JobsBackend?, openResume: (@Sendable (URL) -> Bool)?,
                                 sameQuestion: SameQuestionBackend?) throws -> [String: Any] {
        guard let req = request as? [String: Any], let op = req["op"] as? String else {
            throw BankError("request is not an object with an op")
        }
        switch op {
        case "ping":
            return ["ok": true, "protocol": version, "rev": try store.read().rev]
        case "get":
            let snap = try store.read()
            let rawData = try snap.bank.encoded()
            if rawData.count > NativeMessaging.chunkThreshold {
                let ranges = NativeMessaging.chunkRanges(of: rawData)
                let chunkCount = ranges.count
                let chunkStr = String(decoding: rawData[ranges[0]], as: UTF8.self)
                return [
                    "rev": snap.rev,
                    "chunked": true,
                    "totalBytes": rawData.count,
                    "chunkIndex": 0,
                    "chunkCount": chunkCount,
                    "chunk": chunkStr
                ]
            }
            // Reuse the bytes already encoded for the size check.
            return ["rev": snap.rev, "bank": try JSONSerialization.jsonObject(with: rawData)]
        case "getChunk":
            guard let rev = BankValidator.strictInt(req["rev"]),
                  let index = BankValidator.strictInt(req["chunkIndex"]), index >= 0 else {
                throw BankError("getChunk needs rev and chunkIndex")
            }
            let snap = try store.read()
            if snap.rev != rev {
                return ["conflict": true, "rev": snap.rev]
            }
            let rawData = try snap.bank.encoded()
            let ranges = NativeMessaging.chunkRanges(of: rawData)
            guard index < ranges.count else {
                throw BankError("chunkIndex out of range")
            }
            let chunkStr = String(decoding: rawData[ranges[index]], as: UTF8.self)
            return [
                "rev": snap.rev,
                "chunkIndex": index,
                "chunkCount": ranges.count,
                "chunk": chunkStr
            ]
        case "put":
            guard let base = BankValidator.strictInt(req["baseRev"]), base >= 0 else {
                throw BankError("put needs a non-negative baseRev")
            }
            guard let bank = req["bank"] else { throw BankError("put needs a bank") }
            switch try store.put(baseRev: base, bank: bank) {
            case let .stored(rev, dropped):
                return ["rev": rev, "droppedSecret": dropped]
            case let .conflict(snap):
                let rawData = try snap.bank.encoded()
                if rawData.count > NativeMessaging.chunkThreshold {
                    return ["conflict": true, "rev": snap.rev, "chunked": true]
                }
                return ["conflict": true, "rev": snap.rev, "bank": try snap.bank.jsonObject()]
            }
        case "patch":
            // Validate every row and the delete list before anything is applied: one bad row
            // refuses the whole patch (nothing applied, rev unchanged), never a silent skip.
            let upsertAnswers: [Answer]
            let deleteKeys: [String]
            let baseRev: Int?
            do {
                upsertAnswers = try patchRows(req["upsert"])
                deleteKeys = try patchDeleteKeys(req["delete"])
                baseRev = try patchBaseRev(req["baseRev"])
            } catch {
                return ["error": String(describing: error), "code": "bad_request"]
            }
            do {
                let res = try store.patch(upsert: upsertAnswers, deleteKeys: deleteKeys, baseRev: baseRev)
                return ["ok": true, "rev": res.rev, "droppedSecret": res.droppedSecret]
            } catch SQLiteBankStore.StoreError.conflict(let currentRev) {
                return ["conflict": true, "rev": currentRev]
            }
        case "log":
            try store.log(try activityEntry(req["entry"], source: source))
            return ["ok": true]
        case "jobStart":
            guard let agent else { return ["error": "jobs are not available here", "code": "unsupported"] }
            guard let kind = req["kind"] as? String, let jobKind = JobKind(rawValue: kind) else {
                return ["error": "jobStart needs a known kind", "code": "bad_request"]
            }
            guard let body = req["request"], JSONSerialization.isValidJSONObject(body),
                  let data = try? JSONSerialization.data(withJSONObject: body) else {
                return ["error": "jobStart needs a request object", "code": "bad_request"]
            }
            let reply = agent.start(kind: kind, request: data)
            // Only an event and a count reach the log: never a label, a posting or a draft.
            if reply["job"] != nil {
                let event: ActivityEntry.Event = jobKind.buildsResume ? .resume : .draft
                try? store.log(ActivityEntry(source: source, event: event, counts: [jobKind.buildsResume ? "building" : "started": 1]))
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
        case "jobsIngest":
            guard let jobs = jobs ?? (agent as? JobsBackend) else { return ["error": "jobs are not available here", "code": "unsupported"] }
            let payload: Data
            do { payload = try JobIngest.payload(from: req) } catch {
                return ["error": String(describing: error), "code": "bad_request"]
            }
            // Nothing about the page or the jobs reaches the log, and errors name rules, not text.
            do {
                guard let reply = try JSONSerialization.jsonObject(with: try jobs.ingest(payload: payload)) as? [String: Any] else {
                    return ["error": "vani's ingest reply was not an object", "code": "jobs"]
                }
                return reply
            } catch {
                return ["error": String(describing: error), "code": "jobs"]
            }
        case "sameQuestion":
            guard let labels = SameQuestion.labels(a: req["a"], b: req["b"]) else {
                return ["error": "sameQuestion needs two labels of 1 to \(SameQuestion.maxLabelScalars) characters", "code": "bad_request"]
            }
            // No backend is a reading, not an error: the extension falls back to its own matching.
            // Nothing is logged: not the labels, and not a count per question, which would churn the
            // activity log once per label pair during a fill.
            guard let backend = sameQuestion ?? (agent as? SameQuestionBackend) else {
                return SameQuestion.unavailable("unsupported")
            }
            return backend.compare(labels)
        case "resumePut", "resumeGet", "resumeInfo", "resumeDelete", "resumeOpen":
            return resumeOp(op, req, store: store, source: source, openResume: openResume)
        case "resumeOpenData":
            return resumeOpenData(req, openResume: openResume)
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

    /// An absent field is fine; a present one of the wrong type is not.
    private static func optional<T>(_ raw: [String: Any], _ name: String, _ type: T.Type, row: Int) throws -> T? {
        guard let v = raw[name] else { return nil }
        guard let typed = v as? T else { throw BankError("upsert row \(row): \(name) has the wrong type") }
        return typed
    }

    private static func patchRows(_ raw: Any?) throws -> [Answer] {
        guard let raw else { return [] }
        guard let rows = raw as? [Any] else { throw BankError("patch upsert must be an array") }
        var out: [Answer] = []
        for (i, element) in rows.enumerated() {
            guard let row = element as? [String: Any] else { throw BankError("upsert row \(i) is not an object") }
            guard let key = row["key"] as? String else { throw BankError("upsert row \(i) needs a string key") }
            guard let kindName = row["kind"] as? String, let kind = FieldKind(rawValue: kindName) else {
                throw BankError("upsert row \(i) has an unknown kind")
            }
            guard let value = row["value"] as? [String] else { throw BankError("upsert row \(i) needs a value array of strings") }
            let labels = try optional(row, "labels", [String].self, row: i) ?? []
            let signatures = try optional(row, "signatures", [String].self, row: i) ?? []
            var uses = 0, fills = 0
            for (name, slot) in [("uses", 0), ("fills", 1)] {
                guard let v = row[name] else { continue }
                guard let n = BankValidator.strictInt(v) else {
                    throw BankError("upsert row \(i): \(name) is not an integer")
                }
                if slot == 0 { uses = n } else { fills = n }
            }
            let updatedAt = try optional(row, "updatedAt", String.self, row: i) ?? isoNow()
            let review = try optional(row, "review", Bool.self, row: i) ?? false
            let sens = Classifier.classify(kind: kind, label: ([key] + labels).joined(separator: " "), name: "", autocomplete: "")
            out.append(Answer(key: key, labels: labels, kind: kind, value: value, sensitivity: sens, signatures: signatures,
                              uses: uses, fills: fills, updatedAt: updatedAt, review: review))
        }
        return out
    }

    private static func patchDeleteKeys(_ raw: Any?) throws -> [String] {
        guard let raw else { return [] }
        guard let keys = raw as? [String] else { throw BankError("patch delete must be an array of strings") }
        return keys
    }

    private static func patchBaseRev(_ raw: Any?) throws -> Int? {
        guard let raw else { return nil }
        guard let n = BankValidator.strictInt(raw), n >= 0 else {
            throw BankError("patch baseRev must be a non-negative integer")
        }
        return n
    }

    /// base64 of ResumeStore.maxBytes plus padding slack: a longer string is refused before it is decoded.
    private static let maxResumeBase64 = (ResumeStore.maxBytes + 2) / 3 * 4 + 8

    private static func resumeOp(_ op: String, _ req: [String: Any], store: BankStore, source: Source,
                                 openResume: (@Sendable (URL) -> Bool)?) -> [String: Any] {
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
                // The one bind-and-log (also Resume Studio's "Use for this job"): a single key here, so any
                // failure is thrown and caught below exactly as a failed put was.
                let outcome = try ResumeBinding.bind(data: data, name: name, keys: [key], resumes: resumes, store: store, source: source)
                let e = outcome.bound[0].entry
                return ["ok": true, "key": key, "name": e.name, "bytes": e.bytes, "sha256": e.sha256, "pickedAt": e.pickedAt]
            case "resumeGet":
                guard let (e, data) = try resumes.get(key: key) else { return ["found": false] }
                var out = described(e)
                out["pdf"] = data.base64EncodedString()
                return out
            case "resumeInfo":
                guard let e = try resumes.info(key: key) else { return ["found": false] }
                return described(e)
            case "resumeOpen":
                // No opener: refuse before a copy is written. A read is not logged, and the reply has no PDF.
                guard let openResume else {
                    return ["error": "opening a resume is not available here", "code": "unsupported"]
                }
                guard let (e, data) = try resumes.get(key: key) else { return ["found": false] }
                let url = try ResumeStore.previewCopy(name: e.name, data: data)
                guard openResume(url) else { return ["error": "Preview did not open the resume", "code": "io"] }
                return ["ok": true, "name": e.name, "bytes": e.bytes]
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

    /// Open a PDF the page already holds (a resume just built or chosen). It is checked again
    /// and not stored. The reply names the file and its size; it does not echo the PDF.
    private static func resumeOpenData(_ req: [String: Any], openResume: (@Sendable (URL) -> Bool)?) -> [String: Any] {
        guard let openResume else {
            return ["error": "opening a resume is not available here", "code": "unsupported"]
        }
        guard let name = ResumeStore.checkedName(req["name"]) else {
            return ["error": "resumeOpenData needs a file name ending in .pdf, at most \(ResumeStore.maxNameChars) characters", "code": "bad_request"]
        }
        guard let b64 = req["pdf"] as? String else { return ["error": "resumeOpenData needs the PDF", "code": "bad_request"] }
        if b64.utf8.count > maxResumeBase64 {
            return ["error": "the resume is over \(ResumeStore.maxBytes / 1024) KB", "code": "too_large"]
        }
        guard let data = Data(base64Encoded: b64), !data.isEmpty else {
            return ["error": "the resume is not a PDF", "code": "bad_request"]
        }
        if data.count > ResumeStore.maxBytes {
            return ["error": "the resume is over \(ResumeStore.maxBytes / 1024) KB", "code": "too_large"]
        }
        guard ResumeStore.isPDF(data) else {
            return ["error": "the resume is not a PDF", "code": "bad_request"]
        }
        do {
            let url = try ResumeStore.previewCopy(name: name, data: data)
            guard openResume(url) else { return ["error": "Preview did not open the resume", "code": "io"] }
            return ["ok": true, "name": name, "bytes": data.count]
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
                      let n = BankValidator.strictInt(v), n >= 0, n <= 1_000_000 else {
                    throw BankError("log entry counts malformed")
                }
                out[k] = n
            }
            counts = out
        }
        return ActivityEntry(source: source, event: event, host: host, counts: counts)
    }
}
