import Foundation
import SQLite3

private let SQLITE_TRANSIENT = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

// SQLite-backed storage for the answer bank in the shared App Group container.
// Replaces monolithic JSON serialization with granular indexed lookups and
// row-level atomic mutations while preserving atomic CAS snapshot semantics.
// Uses Apple's built-in libsqlite3 (zero external dependencies).

public final class SQLiteBankStore: @unchecked Sendable {
    public let directory: URL
    public let databaseURL: URL
    private let busyTimeoutMs: Int32 = 5000

    public enum StoreError: Error, CustomStringConvertible {
        case openFailed(String)
        case executeFailed(String)
        case prepareFailed(String)
        case busy(String)
        case conflict(Int)
        case corrupt(String)

        public var description: String {
            switch self {
            case .openFailed(let msg): return "SQLite open failed: \(msg)"
            case .executeFailed(let msg): return "SQLite exec failed: \(msg)"
            case .prepareFailed(let msg): return "SQLite prepare failed: \(msg)"
            case .busy(let msg): return "SQLite busy: \(msg)"
            case .conflict(let rev): return "SQLite CAS conflict at rev \(rev)"
            case .corrupt(let msg): return "SQLite corrupt: \(msg)"
            }
        }
    }

    public init(directory: URL) throws {
        self.directory = directory
        self.databaseURL = directory.appendingPathComponent("bank.sqlite")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try initSchemaAndMigrate()
    }

    // MARK: - SQLite Connection Helpers

    private func openConnection() throws -> OpaquePointer {
        var db: OpaquePointer?
        let flags = SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_FULLMUTEX
        if sqlite3_open_v2(databaseURL.path, &db, flags, nil) != SQLITE_OK {
            let msg = db.map { String(cString: sqlite3_errmsg($0)) } ?? "unknown error"
            if let db { sqlite3_close(db) }
            throw StoreError.openFailed(msg)
        }
        guard let db else { throw StoreError.openFailed("nil database handle") }
        sqlite3_busy_timeout(db, busyTimeoutMs)
        return db
    }

    private func execute(_ sql: String, db: OpaquePointer) throws {
        var err: UnsafeMutablePointer<CChar>?
        if sqlite3_exec(db, sql, nil, nil, &err) != SQLITE_OK {
            let msg = err.map { String(cString: $0) } ?? String(cString: sqlite3_errmsg(db))
            sqlite3_free(err)
            throw StoreError.executeFailed(msg)
        }
    }

    // MARK: - Schema Initialization & Migration

    private func initSchemaAndMigrate() throws {
        let db = try openConnection()
        defer { sqlite3_close(db) }

        try execute("""
            PRAGMA journal_mode = WAL;
            PRAGMA synchronous = NORMAL;
            PRAGMA foreign_keys = ON;

            CREATE TABLE IF NOT EXISTS meta (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS settings (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS answers (
                key TEXT PRIMARY KEY,
                kind TEXT NOT NULL,
                value_json TEXT NOT NULL,
                sensitivity TEXT NOT NULL,
                uses INTEGER NOT NULL DEFAULT 0,
                fills INTEGER NOT NULL DEFAULT 0,
                updated_at TEXT NOT NULL,
                review INTEGER NOT NULL DEFAULT 0
            );

            CREATE TABLE IF NOT EXISTS labels (
                answer_key TEXT NOT NULL REFERENCES answers(key) ON DELETE CASCADE,
                label TEXT NOT NULL,
                PRIMARY KEY (answer_key, label)
            );

            CREATE TABLE IF NOT EXISTS signatures (
                answer_key TEXT NOT NULL REFERENCES answers(key) ON DELETE CASCADE,
                signature TEXT NOT NULL,
                PRIMARY KEY (answer_key, signature)
            );

            CREATE INDEX IF NOT EXISTS idx_labels_label ON labels(label);
            CREATE INDEX IF NOT EXISTS idx_signatures_sig ON signatures(signature);
            CREATE INDEX IF NOT EXISTS idx_answers_updated ON answers(updated_at DESC);
        """, db: db)

        // Initialize revision in meta if not present
        try execute("INSERT OR IGNORE INTO meta (key, value) VALUES ('rev', '0');", db: db)

        // Check if migration from bank.json is needed
        let jsonURL = directory.appendingPathComponent("bank.json")
        let count = try rowCount(table: "answers", db: db)
        if count == 0 && FileManager.default.fileExists(atPath: jsonURL.path) {
            try migrateFromJSON(url: jsonURL, db: db)
        }
    }

    private func rowCount(table: String, db: OpaquePointer) throws -> Int {
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, "SELECT COUNT(*) FROM \(table);", -1, &stmt, nil) == SQLITE_OK else {
            throw StoreError.prepareFailed("rowCount prepare failed")
        }
        defer { sqlite3_finalize(stmt) }
        if sqlite3_step(stmt) == SQLITE_ROW {
            return Int(sqlite3_column_int(stmt, 0))
        }
        return 0
    }

    private func migrateFromJSON(url: URL, db: OpaquePointer) throws {
        guard let data = try? Data(contentsOf: url),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let validated = try? BankValidator.validate(obj["bank"] ?? obj) else {
            return
        }
        let rev = (obj["rev"] as? NSNumber)?.intValue ?? 1
        try execute("BEGIN IMMEDIATE TRANSACTION;", db: db)
        do {
            try execute("UPDATE meta SET value = '\(rev)' WHERE key = 'rev';", db: db)
            try writeSettings(validated.bank.settings, db: db)
            for (_, answer) in validated.bank.answers {
                try insertOrReplaceAnswer(answer, db: db)
            }
            try execute("COMMIT;", db: db)
        } catch {
            try? execute("ROLLBACK;", db: db)
            throw error
        }
    }

    // MARK: - Revision Management

    public func currentRevision() throws -> Int {
        let db = try openConnection()
        defer { sqlite3_close(db) }
        return try getRevision(db: db)
    }

    private func getRevision(db: OpaquePointer) throws -> Int {
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, "SELECT value FROM meta WHERE key = 'rev';", -1, &stmt, nil) == SQLITE_OK else {
            throw StoreError.prepareFailed("getRevision prepare failed")
        }
        defer { sqlite3_finalize(stmt) }
        if sqlite3_step(stmt) == SQLITE_ROW, let text = sqlite3_column_text(stmt, 0) {
            return Int(String(cString: text)) ?? 0
        }
        return 0
    }

    private func bumpRevision(db: OpaquePointer) throws -> Int {
        let rev = try getRevision(db: db) + 1
        try execute("UPDATE meta SET value = '\(rev)' WHERE key = 'rev';", db: db)
        return rev
    }

    // MARK: - Settings

    private func writeSettings(_ settings: Settings, db: OpaquePointer) throws {
        let captureVal = settings.captureSelfId ? "1" : "0"
        let hostsJSON = (try? JSONSerialization.data(withJSONObject: settings.disabledHosts)).flatMap { String(data: $0, encoding: .utf8) } ?? "[]"

        var stmt: OpaquePointer?
        let sql = "INSERT OR REPLACE INTO settings (key, value) VALUES ('captureSelfId', ?), ('disabledHosts', ?);"
        guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else {
            throw StoreError.prepareFailed("writeSettings prepare failed")
        }
        defer { sqlite3_finalize(stmt) }
        sqlite3_bind_text(stmt, 1, captureVal, -1, SQLITE_TRANSIENT)
        sqlite3_bind_text(stmt, 2, hostsJSON, -1, SQLITE_TRANSIENT)
        if sqlite3_step(stmt) != SQLITE_DONE {
            throw StoreError.executeFailed("writeSettings failed")
        }
    }

    private func readSettings(db: OpaquePointer) -> Settings {
        var capture = false
        var disabled: [String] = []
        var stmt: OpaquePointer?
        if sqlite3_prepare_v2(db, "SELECT key, value FROM settings;", -1, &stmt, nil) == SQLITE_OK {
            defer { sqlite3_finalize(stmt) }
            while sqlite3_step(stmt) == SQLITE_ROW {
                guard let k = sqlite3_column_text(stmt, 0), let v = sqlite3_column_text(stmt, 1) else { continue }
                let key = String(cString: k)
                let val = String(cString: v)
                if key == "captureSelfId" {
                    capture = val == "1"
                } else if key == "disabledHosts", let data = val.data(using: .utf8),
                          let arr = try? JSONSerialization.jsonObject(with: data) as? [String] {
                    disabled = arr
                }
            }
        }
        return Settings(captureSelfId: capture, disabledHosts: disabled)
    }

    // MARK: - Answer Operations

    private func insertOrReplaceAnswer(_ a: Answer, db: OpaquePointer) throws {
        let valData = try JSONSerialization.data(withJSONObject: a.value)
        let valStr = String(decoding: valData, as: UTF8.self)

        var stmt: OpaquePointer?
        let sql = """
            INSERT OR REPLACE INTO answers (key, kind, value_json, sensitivity, uses, fills, updated_at, review)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?);
        """
        guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else {
            throw StoreError.prepareFailed("insertAnswer prepare failed")
        }
        defer { sqlite3_finalize(stmt) }

        sqlite3_bind_text(stmt, 1, a.key, -1, SQLITE_TRANSIENT)
        sqlite3_bind_text(stmt, 2, a.kind.rawValue, -1, SQLITE_TRANSIENT)
        sqlite3_bind_text(stmt, 3, valStr, -1, SQLITE_TRANSIENT)
        sqlite3_bind_text(stmt, 4, a.sensitivity.rawValue, -1, SQLITE_TRANSIENT)
        sqlite3_bind_int(stmt, 5, Int32(a.uses))
        sqlite3_bind_int(stmt, 6, Int32(a.fills))
        sqlite3_bind_text(stmt, 7, a.updatedAt, -1, SQLITE_TRANSIENT)
        sqlite3_bind_int(stmt, 8, a.review ? 1 : 0)

        guard sqlite3_step(stmt) == SQLITE_DONE else {
            throw StoreError.executeFailed("insertAnswer step failed: \(String(cString: sqlite3_errmsg(db)))")
        }

        // Labels
        try execute("DELETE FROM labels WHERE answer_key = '\(a.key.replacingOccurrences(of: "'", with: "''"))';", db: db)
        if !a.labels.isEmpty {
            var lblStmt: OpaquePointer?
            if sqlite3_prepare_v2(db, "INSERT OR IGNORE INTO labels (answer_key, label) VALUES (?, ?);", -1, &lblStmt, nil) == SQLITE_OK {
                defer { sqlite3_finalize(lblStmt) }
                for label in a.labels {
                    sqlite3_bind_text(lblStmt, 1, a.key, -1, SQLITE_TRANSIENT)
                    sqlite3_bind_text(lblStmt, 2, label, -1, SQLITE_TRANSIENT)
                    _ = sqlite3_step(lblStmt)
                    sqlite3_reset(lblStmt)
                }
            }
        }

        // Signatures
        try execute("DELETE FROM signatures WHERE answer_key = '\(a.key.replacingOccurrences(of: "'", with: "''"))';", db: db)
        if !a.signatures.isEmpty {
            var sigStmt: OpaquePointer?
            if sqlite3_prepare_v2(db, "INSERT OR IGNORE INTO signatures (answer_key, signature) VALUES (?, ?);", -1, &sigStmt, nil) == SQLITE_OK {
                defer { sqlite3_finalize(sigStmt) }
                for sig in a.signatures {
                    sqlite3_bind_text(sigStmt, 1, a.key, -1, SQLITE_TRANSIENT)
                    sqlite3_bind_text(sigStmt, 2, sig, -1, SQLITE_TRANSIENT)
                    _ = sqlite3_step(sigStmt)
                    sqlite3_reset(sigStmt)
                }
            }
        }
    }

    private func deleteAnswer(key: String, db: OpaquePointer) throws {
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, "DELETE FROM answers WHERE key = ?;", -1, &stmt, nil) == SQLITE_OK else {
            throw StoreError.prepareFailed("deleteAnswer prepare failed")
        }
        defer { sqlite3_finalize(stmt) }
        sqlite3_bind_text(stmt, 1, key, -1, SQLITE_TRANSIENT)
        _ = sqlite3_step(stmt)
        try execute("DELETE FROM labels WHERE answer_key = '\(key.replacingOccurrences(of: "'", with: "''"))';", db: db)
        try execute("DELETE FROM signatures WHERE answer_key = '\(key.replacingOccurrences(of: "'", with: "''"))';", db: db)
    }

    /// `whereClause` is SQL text built by this file with `?` placeholders only; user text goes in `binds`.
    private func loadAnswersMap(whereClause: String = "", binds: [String] = [], db: OpaquePointer) throws -> [String: Answer] {
        var answers: [String: Answer] = [:]
        let sql = "SELECT key, kind, value_json, sensitivity, uses, fills, updated_at, review FROM answers \(whereClause);"
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else {
            throw StoreError.prepareFailed("loadAnswers prepare failed")
        }
        defer { sqlite3_finalize(stmt) }
        for (i, text) in binds.enumerated() {
            sqlite3_bind_text(stmt, Int32(i + 1), text, -1, SQLITE_TRANSIENT)
        }

        while sqlite3_step(stmt) == SQLITE_ROW {
            guard let k = sqlite3_column_text(stmt, 0),
                  let kindStr = sqlite3_column_text(stmt, 1),
                  let valStr = sqlite3_column_text(stmt, 2),
                  let sensStr = sqlite3_column_text(stmt, 3),
                  let updatedStr = sqlite3_column_text(stmt, 6) else { continue }

            let key = String(cString: k)
            let kind = FieldKind(rawValue: String(cString: kindStr)) ?? .text
            let valData = Data(String(cString: valStr).utf8)
            let value = (try? JSONSerialization.jsonObject(with: valData) as? [String]) ?? []
            let sensitivity = Sensitivity(rawValue: String(cString: sensStr)) ?? .standard
            let uses = Int(sqlite3_column_int(stmt, 4))
            let fills = Int(sqlite3_column_int(stmt, 5))
            let updatedAt = String(cString: updatedStr)
            let review = sqlite3_column_int(stmt, 7) == 1

            answers[key] = Answer(
                key: key,
                labels: [],
                kind: kind,
                value: value,
                sensitivity: sensitivity,
                signatures: [],
                uses: uses,
                fills: fills,
                updatedAt: updatedAt,
                review: review
            )
        }

        // Attach labels
        var lblStmt: OpaquePointer?
        if sqlite3_prepare_v2(db, "SELECT answer_key, label FROM labels ORDER BY rowid ASC;", -1, &lblStmt, nil) == SQLITE_OK {
            defer { sqlite3_finalize(lblStmt) }
            while sqlite3_step(lblStmt) == SQLITE_ROW {
                if let k = sqlite3_column_text(lblStmt, 0), let l = sqlite3_column_text(lblStmt, 1) {
                    let key = String(cString: k)
                    let label = String(cString: l)
                    answers[key]?.labels.append(label)
                }
            }
        }

        // Attach signatures
        var sigStmt: OpaquePointer?
        if sqlite3_prepare_v2(db, "SELECT answer_key, signature FROM signatures ORDER BY rowid ASC;", -1, &sigStmt, nil) == SQLITE_OK {
            defer { sqlite3_finalize(sigStmt) }
            while sqlite3_step(sigStmt) == SQLITE_ROW {
                if let k = sqlite3_column_text(sigStmt, 0), let s = sqlite3_column_text(sigStmt, 1) {
                    let key = String(cString: k)
                    let sig = String(cString: s)
                    answers[key]?.signatures.append(sig)
                }
            }
        }

        return answers
    }

    // MARK: - Public Read / Snapshot API (CAS & Backward Compatibility)

    public func read() throws -> Snapshot {
        let db = try openConnection()
        defer { sqlite3_close(db) }

        let rev = try getRevision(db: db)
        let settings = readSettings(db: db)
        let answers = try loadAnswersMap(db: db)
        return Snapshot(rev: rev, bank: Bank(answers: answers, settings: settings))
    }

    public func put(baseRev: Int, bank raw: Any) throws -> PutResult {
        let checked = try BankValidator.validate(raw)
        let db = try openConnection()
        defer { sqlite3_close(db) }

        try execute("BEGIN IMMEDIATE TRANSACTION;", db: db)
        do {
            let currentRev = try getRevision(db: db)
            if currentRev != baseRev {
                let currentSnap = Snapshot(rev: currentRev, bank: Bank(answers: try loadAnswersMap(db: db), settings: readSettings(db: db)))
                try execute("ROLLBACK;", db: db)
                return .conflict(currentSnap)
            }

            try execute("DELETE FROM labels;", db: db)
            try execute("DELETE FROM signatures;", db: db)
            try execute("DELETE FROM answers;", db: db)

            try writeSettings(checked.bank.settings, db: db)
            for (_, answer) in checked.bank.answers {
                try insertOrReplaceAnswer(answer, db: db)
            }

            let nextRev = try bumpRevision(db: db)
            try execute("COMMIT;", db: db)
            return .stored(rev: nextRev, droppedSecret: checked.droppedSecret)
        } catch {
            try? execute("ROLLBACK;", db: db)
            throw error
        }
    }

    /// Replace every row with the snapshot and set the revision to the snapshot's, in one transaction.
    /// This is how the mirror is brought back in line with bank.json; `put` is a compare-and-swap
    /// and would refuse exactly when the mirror has drifted.
    public func resync(_ snapshot: Snapshot) throws {
        let db = try openConnection()
        defer { sqlite3_close(db) }

        try execute("BEGIN IMMEDIATE TRANSACTION;", db: db)
        do {
            try execute("DELETE FROM labels;", db: db)
            try execute("DELETE FROM signatures;", db: db)
            try execute("DELETE FROM answers;", db: db)
            try writeSettings(snapshot.bank.settings, db: db)
            for (_, answer) in snapshot.bank.answers {
                try insertOrReplaceAnswer(answer, db: db)
            }
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, "INSERT OR REPLACE INTO meta (key, value) VALUES ('rev', ?);", -1, &stmt, nil) == SQLITE_OK else {
                throw StoreError.prepareFailed("resync prepare failed")
            }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, String(snapshot.rev), -1, SQLITE_TRANSIENT)
            guard sqlite3_step(stmt) == SQLITE_DONE else {
                throw StoreError.executeFailed("resync revision failed: \(String(cString: sqlite3_errmsg(db)))")
            }
            try execute("COMMIT;", db: db)
        } catch {
            try? execute("ROLLBACK;", db: db)
            throw error
        }
    }

    // MARK: - Granular Patch API (Row-Level Delta Updates)

    /// The rule for rows arriving in a patch: a secret row is dropped and counted; the rest keep the
    /// classifier's sensitivity. One owner, so the SQLite and bank.json paths cannot drift apart.
    static func screen(_ upsert: [Answer]) -> (kept: [Answer], droppedSecret: Int) {
        var kept: [Answer] = []
        var droppedSecret = 0
        for a in upsert {
            let sens = Classifier.classify(kind: a.kind, label: ([a.key] + a.labels).joined(separator: " "), name: "", autocomplete: "")
            if sens == .secret {
                droppedSecret += 1
                continue
            }
            var clean = a
            clean.sensitivity = sens
            kept.append(clean)
        }
        return (kept, droppedSecret)
    }

    public struct PatchResult: Equatable, Sendable {
        public let rev: Int
        public let droppedSecret: Int
    }

    public func patch(upsert: [Answer], deleteKeys: [String], baseRev: Int? = nil) throws -> PatchResult {
        let db = try openConnection()
        defer { sqlite3_close(db) }

        let (validAnswers, droppedSecret) = Self.screen(upsert)

        try execute("BEGIN IMMEDIATE TRANSACTION;", db: db)
        do {
            let currentRev = try getRevision(db: db)
            if let base = baseRev, base != currentRev {
                try execute("ROLLBACK;", db: db)
                throw StoreError.conflict(currentRev)
            }

            for key in deleteKeys {
                try deleteAnswer(key: key, db: db)
            }
            for ans in validAnswers {
                try insertOrReplaceAnswer(ans, db: db)
            }

            let nextRev = try bumpRevision(db: db)
            try execute("COMMIT;", db: db)
            return PatchResult(rev: nextRev, droppedSecret: droppedSecret)
        } catch {
            try? execute("ROLLBACK;", db: db)
            throw error
        }
    }

    // MARK: - Granular Candidate Query API (Indexed Lookup for Autofill)

    public func queryCandidates(signatures: [String] = [], keys: [String] = [], labels: [String] = []) throws -> [Answer] {
        let db = try openConnection()
        defer { sqlite3_close(db) }

        var matchedKeys = Set<String>()

        // 1. Direct key lookups
        for key in keys {
            var stmt: OpaquePointer?
            if sqlite3_prepare_v2(db, "SELECT key FROM answers WHERE key = ?;", -1, &stmt, nil) == SQLITE_OK {
                defer { sqlite3_finalize(stmt) }
                sqlite3_bind_text(stmt, 1, key, -1, SQLITE_TRANSIENT)
                if sqlite3_step(stmt) == SQLITE_ROW, let k = sqlite3_column_text(stmt, 0) {
                    matchedKeys.insert(String(cString: k))
                }
            }
        }

        // 2. Signatures lookup
        for sig in signatures {
            var stmt: OpaquePointer?
            if sqlite3_prepare_v2(db, "SELECT answer_key FROM signatures WHERE signature = ?;", -1, &stmt, nil) == SQLITE_OK {
                defer { sqlite3_finalize(stmt) }
                sqlite3_bind_text(stmt, 1, sig, -1, SQLITE_TRANSIENT)
                while sqlite3_step(stmt) == SQLITE_ROW {
                    if let k = sqlite3_column_text(stmt, 0) {
                        matchedKeys.insert(String(cString: k))
                    }
                }
            }
        }

        // 3. Labels lookup
        for label in labels {
            var stmt: OpaquePointer?
            if sqlite3_prepare_v2(db, "SELECT answer_key FROM labels WHERE label = ?;", -1, &stmt, nil) == SQLITE_OK {
                defer { sqlite3_finalize(stmt) }
                sqlite3_bind_text(stmt, 1, label, -1, SQLITE_TRANSIENT)
                while sqlite3_step(stmt) == SQLITE_ROW {
                    if let k = sqlite3_column_text(stmt, 0) {
                        matchedKeys.insert(String(cString: k))
                    }
                }
            }
        }

        guard !matchedKeys.isEmpty else { return [] }

        // Fetch full candidate answers
        let escaped = matchedKeys.map { "'\($0.replacingOccurrences(of: "'", with: "''"))'" }.joined(separator: ",")
        let map = try loadAnswersMap(whereClause: "WHERE key IN (\(escaped))", db: db)
        return Array(map.values)
    }

    // MARK: - Granular List & Search API (Paginated UI)

    public struct ListResult: Equatable, Sendable {
        public let total: Int
        public let rev: Int
        public let answers: [Answer]
    }

    private static func likeEscaped(_ s: String) -> String {
        s.replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "%", with: "\\%")
            .replacingOccurrences(of: "_", with: "\\_")
    }

    /// The text as the value_json writer (JSONSerialization over the value array) encodes it inside a
    /// string: the term is encoded as a one-element array and the `["` / `"]` frame is dropped. If the
    /// encoder refuses the term, the raw term is returned, which can only under-match.
    private static func jsonStringBody(_ s: String) -> String {
        guard let data = try? JSONSerialization.data(withJSONObject: [s]) else { return s }
        let framed = String(decoding: data, as: UTF8.self)
        guard framed.hasPrefix("[\""), framed.hasSuffix("\"]") else { return s }
        return String(framed.dropFirst(2).dropLast(2))
    }

    public func listAnswers(offset: Int = 0, limit: Int = 50, search: String? = nil) throws -> ListResult {
        let (offset, limit) = ListPaging.clamped(offset: offset, limit: limit)
        let db = try openConnection()
        defer { sqlite3_close(db) }

        let rev = try getRevision(db: db)
        var whereClause = ""
        var binds: [String] = []
        if let s = search, !s.trimmingCharacters(in: .whitespaces).isEmpty {
            // The text is bound, never spliced into SQL; % _ and \ in it match themselves.
            let pattern = "%\(Self.likeEscaped(s))%"
            // value_json holds the JSON-encoded array, so the term is matched as that encoder writes it.
            let valuePattern = "%\(Self.likeEscaped(Self.jsonStringBody(s)))%"
            whereClause = """
                WHERE key LIKE ? ESCAPE '\\' OR value_json LIKE ? ESCAPE '\\' \
                OR key IN (SELECT answer_key FROM labels WHERE label LIKE ? ESCAPE '\\')
                """
            binds = [pattern, valuePattern, pattern]
        }

        var total = 0
        var cntStmt: OpaquePointer?
        if sqlite3_prepare_v2(db, "SELECT COUNT(*) FROM answers \(whereClause);", -1, &cntStmt, nil) == SQLITE_OK {
            defer { sqlite3_finalize(cntStmt) }
            for (i, text) in binds.enumerated() {
                sqlite3_bind_text(cntStmt, Int32(i + 1), text, -1, SQLITE_TRANSIENT)
            }
            if sqlite3_step(cntStmt) == SQLITE_ROW {
                total = Int(sqlite3_column_int(cntStmt, 0))
            }
        }

        let pageClause = "\(whereClause) ORDER BY updated_at DESC LIMIT \(limit) OFFSET \(offset)"
        let map = try loadAnswersMap(whereClause: pageClause, binds: binds, db: db)
        let sorted = map.values.sorted { $0.updatedAt > $1.updatedAt }
        return ListResult(total: total, rev: rev, answers: sorted)
    }
}
