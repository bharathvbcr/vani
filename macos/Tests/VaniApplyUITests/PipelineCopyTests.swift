import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Copy failures through the model: it asks vani for the report and hands the text to
/// the pasteboard writer, and when vani cannot answer nothing is written and the
/// reason is kept for the tab to show.
@MainActor
final class PipelineCopyTests: XCTestCase {
    private func vani(_ body: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-copy-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let url = dir.appendingPathComponent("vani")
        try Data("#!/bin/sh\n\(body)\n".utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        return url
    }

    private func model(_ vani: URL, timeout: TimeInterval = 10) throws -> PipelineModel {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "vani-copy-tests-\(UUID().uuidString)"))
        return PipelineModel(client: { PipelineClient(vaniBinary: vani, timeout: timeout) }, defaults: defaults)
    }

    func testTheReportsTextIsWhatIsWritten() async throws {
        // printf %s keeps the JSON's backslash-n escapes as written (echo would expand them).
        let v = try vani(#"printf '%s\n' '{"version":1,"id":"20261022T090000.123Z-1","has_failures":true,"text":"Vani run report\nwd:x (X): 429\n"}'"#)
        let m = try model(v)
        var written: [String] = []
        await m.copyFailures { written.append($0) }
        XCTAssertEqual(written, ["Vani run report\nwd:x (X): 429\n"])
        XCTAssertEqual(m.failureCopy, .copied)
        m.resetFailureCopy()
        XCTAssertEqual(m.failureCopy, .idle)
    }

    /// An older vani, a hang and an empty report each write nothing and say why.
    func testWhenVaniCannotAnswerNothingIsCopiedAndTheReasonIsKept() async throws {
        var written: [String] = []
        let cases: [(String, TimeInterval, String)] = [
            (#"echo 'vani: unknown command "runs"' >&2; exit 2"#, 10, "cannot report failures"),
            ("sleep 30", 1, "did not finish within 1 s"),
            (#"echo '{"version":1,"id":"i","has_failures":false,"text":""}'"#, 10, "empty failure report"),
            (#"echo '{"error":"no run has been recorded yet"}'; exit 1"#, 10, "no run has been recorded yet"),
        ]
        for (body, timeout, reason) in cases {
            let m = try model(try vani(body), timeout: timeout)
            await m.copyFailures { written.append($0) }
            guard case let .failed(message) = m.failureCopy else { return XCTFail("\(body): \(m.failureCopy)") }
            XCTAssertTrue(message.contains(reason), "\(body): \(message)")
            XCTAssertTrue(message.hasPrefix("Could not copy the failures:"), message)
            // A failure is dismissed, and a new attempt starts clean.
            m.resetFailureCopy()
            XCTAssertEqual(m.failureCopy, .idle)
        }
        XCTAssertTrue(written.isEmpty, "the pasteboard was written: \(written)")
    }
}
