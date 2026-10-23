import Foundation
import XCTest
@testable import VaniApplyKit

/// Keeping the previous manual run's log, and which run files the tab offers.
final class RunLogsTests: XCTestCase {
    private let fm = FileManager.default

    private func state() throws -> String { try tempDir(self).path }

    private func writeLog(_ state: String, _ text: String, modified: Date? = nil) throws -> URL {
        let log = ManualRunLauncher.logURL(stateDir: state)
        try fm.createDirectory(at: log.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data(text.utf8).write(to: log)
        if let modified { try fm.setAttributes([.modificationDate: modified], ofItemAtPath: log.path) }
        return log
    }

    private func rotated(_ state: String) throws -> [String] {
        let folder = RunFiles.historyFolder(stateDir: state)
        guard fm.fileExists(atPath: folder.path) else { return [] }
        return try fm.contentsOfDirectory(atPath: folder.path).filter { $0.hasPrefix("manual-") }.sorted()
    }

    // MARK: rotation

    func testThePreviousLogIsMovedNotOverwritten() throws {
        let s = try state()
        let when = Date(timeIntervalSince1970: 1_792_000_000) // 2026-10-14T17:46:40Z
        let log = try writeLog(s, "vani: board eightfold:microsoft: http 429\n", modified: when)
        try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: log.path) // as VaniProcess.launch creates it
        let result = try ManualRunLog.rotate(stateDir: s)
        guard case let .kept(url) = result else { return XCTFail("\(result)") }
        XCTAssertEqual(url.lastPathComponent, "manual-20261014T174640Z.log")
        XCTAssertEqual(url.deletingLastPathComponent().path, RunFiles.historyFolder(stateDir: s).path)
        XCTAssertEqual(try String(contentsOf: url, encoding: .utf8), "vani: board eightfold:microsoft: http 429\n")
        XCTAssertFalse(fm.fileExists(atPath: log.path), "the log is moved, so the next run opens a fresh one")
        let mode = try fm.attributesOfItem(atPath: url.path)[.posixPermissions] as? Int
        XCTAssertEqual(mode, 0o600, "a rotated log keeps its owner-only mode")
        let folderMode = try fm.attributesOfItem(atPath: url.deletingLastPathComponent().path)[.posixPermissions] as? Int
        XCTAssertEqual(folderMode, 0o700)
    }

    func testNoLogAndEmptyLogHaveNothingToKeep() throws {
        let s = try state()
        XCTAssertEqual(try ManualRunLog.rotate(stateDir: s), .nothingToKeep)
        XCTAssertTrue(try rotated(s).isEmpty || !fm.fileExists(atPath: RunFiles.historyFolder(stateDir: s).path))
        let log = try writeLog(s, "")
        XCTAssertEqual(try ManualRunLog.rotate(stateDir: s), .nothingToKeep)
        XCTAssertFalse(fm.fileExists(atPath: log.path), "an empty log is not worth a history slot")
        XCTAssertFalse(fm.fileExists(atPath: RunFiles.historyFolder(stateDir: s).path))
    }

    /// A symlink at manual.log is not rotated (nor followed): the next run's open refuses it.
    func testASymlinkIsLeftAlone() throws {
        let s = try state()
        let target = try tempDir(self).appendingPathComponent("elsewhere")
        try Data("keep".utf8).write(to: target)
        let log = ManualRunLauncher.logURL(stateDir: s)
        try fm.createDirectory(at: log.deletingLastPathComponent(), withIntermediateDirectories: true)
        try fm.createSymbolicLink(at: log, withDestinationURL: target)
        XCTAssertEqual(try ManualRunLog.rotate(stateDir: s), .nothingToKeep)
        XCTAssertNotNil(try? fm.destinationOfSymbolicLink(atPath: log.path))
        XCTAssertEqual(try String(contentsOf: target, encoding: .utf8), "keep")
        // A directory named manual.log likewise.
        try fm.removeItem(at: log)
        try fm.createDirectory(at: log, withIntermediateDirectories: false)
        XCTAssertEqual(try ManualRunLog.rotate(stateDir: s), .nothingToKeep)
        XCTAssertTrue(fm.fileExists(atPath: log.path))
    }

    func testTwoLogsFromTheSameSecondBothSurviveInOrder() throws {
        let s = try state()
        let when = Date(timeIntervalSince1970: 1_792_000_000)
        for text in ["one", "two", "three"] {
            _ = try writeLog(s, text, modified: when)
            try ManualRunLog.rotate(stateDir: s)
        }
        let names = try rotated(s)
        XCTAssertEqual(names, ["manual-20261014T174640Z.log", "manual-20261014T174640Z~02.log", "manual-20261014T174640Z~03.log"])
        XCTAssertEqual(try String(contentsOf: RunFiles.historyFolder(stateDir: s).appendingPathComponent(names[2]), encoding: .utf8), "three")
    }

    func testOnlyTheNewestLogsAreKept() throws {
        let s = try state()
        for i in 0..<7 {
            _ = try writeLog(s, "run \(i)", modified: Date(timeIntervalSince1970: 1_792_000_000 + Double(i) * 3600))
            try ManualRunLog.rotate(stateDir: s, maxKept: 3)
        }
        let names = try rotated(s)
        XCTAssertEqual(names.count, 3)
        let folder = RunFiles.historyFolder(stateDir: s)
        XCTAssertEqual(try names.map { try String(contentsOf: folder.appendingPathComponent($0), encoding: .utf8) }, ["run 4", "run 5", "run 6"])
    }

    func testSizeBoundKeepsTheNewestAndRemovesAnOversizedLog() throws {
        let s = try state()
        let ten = String(repeating: "x", count: 10)
        for i in 0..<4 {
            _ = try writeLog(s, ten, modified: Date(timeIntervalSince1970: 1_792_000_000 + Double(i) * 3600))
            try ManualRunLog.rotate(stateDir: s, maxBytes: 25)
        }
        XCTAssertEqual(try rotated(s).count, 2, "room for two ten-byte logs under 25 bytes")
        // A single log over the bound is not kept at all; nothing else is disturbed.
        _ = try writeLog(s, String(repeating: "y", count: 100))
        XCTAssertEqual(try ManualRunLog.rotate(stateDir: s, maxBytes: 25), .tooLarge(100))
        XCTAssertFalse(fm.fileExists(atPath: ManualRunLauncher.logURL(stateDir: s).path))
        XCTAssertEqual(try rotated(s).count, 2)
    }

    /// vani's run records live in the same folder and are not this app's to prune.
    func testPruningLeavesVanisRecordsAndOtherFilesAlone() throws {
        let s = try state()
        let folder = RunFiles.historyFolder(stateDir: s)
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        let others = ["20261021T090000.000Z-77.json", "notes.txt", "20261021T090000.000Z-77.log", "manual-20250101T000000Z.json"]
        for name in others { try Data("keep".utf8).write(to: folder.appendingPathComponent(name)) }
        for i in 0..<5 {
            _ = try writeLog(s, "run \(i)", modified: Date(timeIntervalSince1970: 1_792_000_000 + Double(i) * 3600))
            try ManualRunLog.rotate(stateDir: s, maxKept: 1)
        }
        for name in others {
            XCTAssertEqual(try? String(contentsOf: folder.appendingPathComponent(name), encoding: .utf8), "keep", name)
        }
        XCTAssertEqual(try rotated(s).filter { $0.hasSuffix(".log") }.count, 1)
    }

    /// The launcher rotates just before the run opens its log, and only for a run that starts.
    func testStartingARunKeepsThePreviousRunsLog() throws {
        let s = try state()
        _ = try writeLog(s, "the previous run's output\n", modified: Date(timeIntervalSince1970: 1_792_000_000))
        let vani = try tempDir(self).appendingPathComponent("vani")
        try Data("#!/bin/sh\necho 'vani: this run'\n".utf8).write(to: vani)
        try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        let runner = DraftRunner(vaniBinary: vani, stateDir: s, keys: .noKey, decisionKey: { nil })
        let exited = expectation(description: "run exited")
        _ = try ManualRunLauncher(runner: runner).start(ManualRunOptions(), stateDir: s) { _ in exited.fulfill() }
        wait(for: [exited], timeout: 10)
        let kept = RunFiles.historyFolder(stateDir: s).appendingPathComponent("manual-20261014T174640Z.log")
        XCTAssertEqual(try String(contentsOf: kept, encoding: .utf8), "the previous run's output\n")
        let current = try String(contentsOf: ManualRunLauncher.logURL(stateDir: s), encoding: .utf8)
        XCTAssertTrue(current.contains("vani: this run"))
        XCTAssertFalse(current.contains("previous run"))
    }

    func testARefusedRunDoesNotRotate() throws {
        let s = try state()
        let log = try writeLog(s, "kept in place\n")
        let runner = DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"), keys: .noKey, decisionKey: { nil })
        XCTAssertThrowsError(try ManualRunLauncher(runner: runner).start(ManualRunOptions(advance: -1), stateDir: s) { _ in })
        XCTAssertEqual(try String(contentsOf: log, encoding: .utf8), "kept in place\n", "options refused before anything starts")
        XCTAssertTrue(try rotated(s).isEmpty || !fm.fileExists(atPath: RunFiles.historyFolder(stateDir: s).path))
    }

    // MARK: naming a rotated log by its run

    private let runStart = Date(timeIntervalSince1970: 1_792_000_000) // 2026-10-14T17:46:40Z
    private let runID = "20261014T174640.123Z-4242"

    /// last.json as vani writes it (Go's RFC 3339 with nanoseconds), plus whatever `extra` overrides.
    private func writeRecord(_ s: String, log: String?, id: String? = nil, finished: String? = "2026-10-14T17:49:40.987654321Z",
                             extra: [String: Any] = [:]) throws {
        var rec: [String: Any] = ["v": 1, "pid": 4242, "started_at": "2026-10-14T17:46:40.123456789Z", "outcome": "ok"]
        rec["id"] = id ?? runID
        if let log { rec["log"] = log }
        if let finished { rec["finished_at"] = finished }
        rec.merge(extra) { _, new in new }
        let url = RunFiles.record(stateDir: s)
        try fm.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSONSerialization.data(withJSONObject: rec).write(to: url)
    }

    private func rotatedName(_ s: String, modified: Date? = nil) throws -> String {
        _ = try writeLog(s, "output", modified: modified ?? runStart.addingTimeInterval(120))
        guard case let .kept(url) = try ManualRunLog.rotate(stateDir: s) else { XCTFail("not kept"); return "" }
        return url.lastPathComponent
    }

    func testTheLogOfTheRecordedRunIsKeptUnderItsRunID() throws {
        let s = try state()
        try writeRecord(s, log: ManualRunLauncher.logURL(stateDir: s).path)
        XCTAssertEqual(try rotatedName(s), "manual-\(runID).log")
        let kept = RunFiles.historyFolder(stateDir: s).appendingPathComponent("manual-\(runID).log")
        XCTAssertEqual(try String(contentsOf: kept, encoding: .utf8), "output")
    }

    /// A run still going or one that never wrote its end has no finish to bound the log's last write.
    func testARunWithNoRecordedEndStillOwnsItsLog() throws {
        let s = try state()
        try writeRecord(s, log: ManualRunLauncher.logURL(stateDir: s).path, finished: nil, extra: ["outcome": "running"])
        XCTAssertEqual(try rotatedName(s), "manual-\(runID).log")
    }

    /// Everything that would tie the file to the wrong run keeps the time-stamped name.
    func testALogNoRecordClaimsKeepsItsTimeStampedName() throws {
        let stamped = "manual-20261014T174840Z.log" // the log's last write, 17:48:40Z
        let cases: [(String, (String) throws -> Void)] = [
            ("no record", { _ in }),
            ("an older vani's record: no id, no log", { try self.writeRecord($0, log: nil, extra: ["id": NSNull()]) }),
            ("a record with no log", { try self.writeRecord($0, log: nil) }),
            ("another file's log", { try self.writeRecord($0, log: "/elsewhere/other.log") }),
            ("a relative log path", { try self.writeRecord($0, log: "runs/manual.log") }),
            ("an id that is not a run id", { try self.writeRecord($0, log: ManualRunLauncher.logURL(stateDir: $0).path, id: "../../etc/x") }),
            ("an id of the wrong type", { try self.writeRecord($0, log: ManualRunLauncher.logURL(stateDir: $0).path, extra: ["id": 5]) }),
            ("a log of the wrong type", { try self.writeRecord($0, log: nil, extra: ["log": ["manual.log"]]) }),
            ("an unreadable start", { try self.writeRecord($0, log: ManualRunLauncher.logURL(stateDir: $0).path, extra: ["started_at": "yesterday"]) }),
            ("a log last written long after the run ended", { try self.writeRecord($0, log: ManualRunLauncher.logURL(stateDir: $0).path, finished: "2026-10-14T17:49:40Z") }),
            ("a log last written before the run began", { try self.writeRecord($0, log: ManualRunLauncher.logURL(stateDir: $0).path, extra: ["started_at": "2026-10-14T18:30:00Z"]) }),
            ("a record that is not JSON", { s in
                try self.fm.createDirectory(at: RunFiles.record(stateDir: s).deletingLastPathComponent(), withIntermediateDirectories: true)
                try Data("{nope".utf8).write(to: RunFiles.record(stateDir: s))
            }),
            ("a record that is an array", { s in
                try self.fm.createDirectory(at: RunFiles.record(stateDir: s).deletingLastPathComponent(), withIntermediateDirectories: true)
                try Data("[]".utf8).write(to: RunFiles.record(stateDir: s))
            }),
            ("a record past vani's bound", { s in
                try self.fm.createDirectory(at: RunFiles.record(stateDir: s).deletingLastPathComponent(), withIntermediateDirectories: true)
                try Data(String(repeating: " ", count: RunLogOwner.maxRecordBytes + 1).utf8).write(to: RunFiles.record(stateDir: s))
            }),
        ]
        for (name, arrange) in cases {
            let s = try state()
            try arrange(s)
            // Written 120 s after the run began, or 600 s for the case that ended at 17:49:40.
            let late = name.contains("long after")
            XCTAssertEqual(try rotatedName(s, modified: runStart.addingTimeInterval(late ? 600 : 120)),
                           late ? "manual-20261014T175640Z.log" : stamped, name)
        }
    }

    /// The daemon makes a scheduled run (no log recorded, and last.json now) after
    /// the app's run and before the app rotates its log: the log is still the app
    /// run's, found in the history. A newer claimant of the file wins over it.
    func testALogIsTheNewestClaimantsNotJustLastJSONs() throws {
        let s = try state()
        let manual = ManualRunLauncher.logURL(stateDir: s).path
        let folder = RunFiles.historyFolder(stateDir: s)
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        func history(_ id: String, _ rec: [String: Any]) throws {
            try JSONSerialization.data(withJSONObject: rec).write(to: folder.appendingPathComponent("\(id).json"))
        }
        let appRun = "20261014T174640.123Z-4242"
        try history(appRun, ["v": 1, "id": appRun, "log": manual, "started_at": "2026-10-14T17:46:40.123456789Z",
                             "finished_at": "2026-10-14T17:49:40Z", "outcome": "ok"])
        // The daemon's run: newer, no log; it owns last.json.
        try writeRecord(s, log: nil, id: "20261014T180000.000Z-99", finished: "2026-10-14T18:01:00Z",
                        extra: ["started_at": "2026-10-14T18:00:00Z"])
        XCTAssertEqual(try rotatedName(s), "manual-\(appRun).log")

        // A newer app run that claims the file owns it instead; the log's last write
        // (17:48:40) is before that run began, so it is not attributed to it.
        let s2 = try state()
        let folder2 = RunFiles.historyFolder(stateDir: s2)
        try fm.createDirectory(at: folder2, withIntermediateDirectories: true)
        let manual2 = ManualRunLauncher.logURL(stateDir: s2).path
        let newer = "20261014T190000.000Z-5"
        try JSONSerialization.data(withJSONObject: ["v": 1, "id": newer, "log": manual2, "started_at": "2026-10-14T19:00:00Z", "outcome": "ok"])
            .write(to: folder2.appendingPathComponent("\(newer).json"))
        try writeRecord(s2, log: nil, id: "20261014T180000.000Z-99", finished: "2026-10-14T18:01:00Z",
                        extra: ["started_at": "2026-10-14T18:00:00Z"])
        XCTAssertEqual(try rotatedName(s2), "manual-20261014T174840Z.log")
    }

    /// The name by run id is never taken over an existing file: the second log is kept under its stamp.
    func testARunIDNameAlreadyTakenIsNeverOverwritten() throws {
        let s = try state()
        let folder = RunFiles.historyFolder(stateDir: s)
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        let taken = folder.appendingPathComponent("manual-\(runID).log")
        try Data("the run's own log".utf8).write(to: taken)
        try writeRecord(s, log: ManualRunLauncher.logURL(stateDir: s).path)
        XCTAssertEqual(try rotatedName(s), "manual-20261014T174840Z.log")
        XCTAssertEqual(try String(contentsOf: taken, encoding: .utf8), "the run's own log")
    }

    /// Run-id names and time-stamped names sort together by when the run happened,
    /// so the newest-N rule keeps the newest whichever way a log was named.
    func testPruningKeepsTheNewestAcrossBothNamings() throws {
        let s = try state()
        let folder = RunFiles.historyFolder(stateDir: s)
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        for name in ["manual-20261014T100000Z.log", "manual-20261014T110000.500Z-77.log", "manual-20261014T120000Z.log", "manual-20261014T130000.250Z-78.log"] {
            try Data(name.utf8).write(to: folder.appendingPathComponent(name))
        }
        ManualRunLog.prune(folder: folder, fileManager: fm, maxKept: 2, maxBytes: 1 << 20)
        XCTAssertEqual(try rotated(s), ["manual-20261014T120000Z.log", "manual-20261014T130000.250Z-78.log"])
    }

    /// The launcher tells vani where the run's output goes, and a vani that
    /// ignores the variable still runs.
    func testTheLauncherNamesTheLogToVani() throws {
        let s = try state()
        let vani = try tempDir(self).appendingPathComponent("vani")
        try Data("#!/bin/sh\necho \"vani: log is $VANI_RUN_LOG\"\n".utf8).write(to: vani)
        try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        let runner = DraftRunner(vaniBinary: vani, stateDir: s, keys: .noKey, decisionKey: { nil })
        let exited = expectation(description: "run exited")
        _ = try ManualRunLauncher(runner: runner).start(ManualRunOptions(), stateDir: s) { _ in exited.fulfill() }
        wait(for: [exited], timeout: 10)
        let out = try String(contentsOf: ManualRunLauncher.logURL(stateDir: s), encoding: .utf8)
        XCTAssertTrue(out.contains("vani: log is \(ManualRunLauncher.logURL(stateDir: s).path)"), out)
    }

    // MARK: which files are offered

    func testOnlyExistingRunFilesAreOffered() throws {
        let s = try state()
        XCTAssertTrue(RunFiles.existing(stateDir: s).isEmpty)
        XCTAssertTrue(RunFiles.existing(stateDir: "").isEmpty)

        let runs = URL(fileURLWithPath: s).appendingPathComponent("runs")
        try fm.createDirectory(at: runs, withIntermediateDirectories: true)
        try Data("{}".utf8).write(to: runs.appendingPathComponent("last.json"))
        var files = RunFiles.existing(stateDir: s)
        XCTAssertEqual(files.record?.lastPathComponent, "last.json")
        XCTAssertNil(files.manualLog, "no run was started here yet")
        XCTAssertNil(files.historyFolder)

        _ = try writeLog(s, "x")
        try fm.createDirectory(at: RunFiles.historyFolder(stateDir: s), withIntermediateDirectories: true)
        files = RunFiles.existing(stateDir: s)
        XCTAssertEqual(files.manualLog?.path, ManualRunLauncher.logURL(stateDir: s).path)
        XCTAssertNotNil(files.historyFolder)
        XCTAssertFalse(files.isEmpty)
    }

    /// A folder where a file belongs, or a file where the folder belongs, is not offered as the other.
    func testWrongKindOfEntryIsNotOffered() throws {
        let s = try state()
        let runs = URL(fileURLWithPath: s).appendingPathComponent("runs")
        try fm.createDirectory(at: runs.appendingPathComponent("manual.log"), withIntermediateDirectories: true)
        try Data("not a folder".utf8).write(to: runs.appendingPathComponent("history"))
        let files = RunFiles.existing(stateDir: s)
        XCTAssertNil(files.manualLog)
        XCTAssertNil(files.historyFolder)
    }
}
