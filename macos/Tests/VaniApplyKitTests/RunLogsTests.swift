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
