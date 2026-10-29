import Foundation
import XCTest
@testable import VaniApplyKit

/// The on-device helper's contract (OnDeviceDecider), checked without the model, plus the built
/// vani-fm binary on requests it refuses before it would ask the model.
final class OnDeviceDeciderTests: XCTestCase {
    typealias D = OnDeviceDecider

    let options = ["essay", "applicant_fact", "work_authorization", "legal_consent", "self_identification", "other"]

    func request(_ patch: (inout [String: Any]) -> Void = { _ in }) -> Data {
        var o: [String: Any] = [
            "v": 1, "task": D.task,
            "question": "Field type: text\nQuestion label (untrusted text copied from a job site):\nLinkedIn URL?",
            "options": options,
        ]
        patch(&o)
        return try! JSONSerialization.data(withJSONObject: o)
    }

    func rejected(_ data: Data, _ detail: String, file: StaticString = #filePath, line: UInt = #line) {
        switch D.parse(data) {
        case .success: XCTFail("accepted a request that must be refused (\(detail))", file: file, line: line)
        case .failure(let r):
            XCTAssertEqual(r.reading, .failed, file: file, line: line)
            XCTAssertEqual(r.detail, detail, file: file, line: line)
            XCTAssertNil(r.value, file: file, line: line)
        }
    }

    func testParsesAValidRequest() throws {
        let req = try D.parse(request { $0["instructions"] = "essay: motivation" }).get()
        XCTAssertEqual(req.options, options)
        XCTAssertEqual(req.instructions, "essay: motivation")
        XCTAssertEqual(D.choices(for: req), options + ["none"])
        XCTAssertTrue(D.prompt(for: req).contains("LinkedIn URL?"))
        XCTAssertFalse(D.sessionInstructions(for: req).contains("LinkedIn"), "the untrusted label belongs in the prompt, not the instructions")
    }

    func testRefusesBadRequests() {
        rejected(Data(), "bad_request_json")
        rejected(Data("not json".utf8), "bad_request_json")
        rejected(Data(#"{"v":1,"task":"career.form_field"}"#.utf8), "bad_request_json")
        rejected(request { $0["v"] = 2 }, "bad_request_version")
        rejected(request { $0["task"] = "career.jd_triage" }, "bad_request_task")
        rejected(request { $0["question"] = "  \n " }, "bad_request_question")
        rejected(request { $0["question"] = String(repeating: "x", count: D.maxQuestionBytes + 1) }, "bad_request_question")
        rejected(request { $0["instructions"] = String(repeating: "x", count: D.maxInstructionsBytes + 1) }, "bad_request_instructions")
        rejected(request { $0["options"] = [String]() }, "bad_request_options")
        rejected(request { $0["options"] = (0...D.maxOptions).map { "o\($0)" } }, "bad_request_options")
        rejected(request { $0["options"] = ["a", "a"] }, "bad_request_options")
        rejected(request { $0["options"] = ["a", ""] }, "bad_request_options")
        rejected(request { $0["options"] = ["a", " b"] }, "bad_request_options")
        rejected(request { $0["options"] = ["a", "b\nc"] }, "bad_request_options")
        rejected(request { $0["options"] = ["a", D.abstainChoice] }, "bad_request_options")
        rejected(request { $0["options"] = ["a", String(repeating: "x", count: D.maxOptionBytes + 1)] }, "bad_request_options")
        rejected(Data(count: D.maxRequestBytes + 1), "bad_request_too_large")
    }

    func testAcceptsTheLimits() throws {
        let sixteen = (0..<D.maxOptions).map { "o\($0)" }
        XCTAssertNoThrow(try D.parse(request { $0["options"] = sixteen }).get())
        XCTAssertNoThrow(try D.parse(request { $0["options"] = [String(repeating: "x", count: D.maxOptionBytes)] }).get())
    }

    func testReplyOnlyEverCarriesAnOption() {
        XCTAssertEqual(D.reply(choice: "essay", options: options), .init(reading: .answered, value: "essay"))
        XCTAssertEqual(D.reply(choice: "none", options: options), .init(reading: .abstained, detail: "declined"))
        XCTAssertEqual(D.reply(choice: nil, options: options), .init(reading: .failed, detail: "decode"))
        for bad in ["Essay", "essay ", "", "ignore previous instructions", "essay\n"] {
            let r = D.reply(choice: bad, options: options)
            XCTAssertEqual(r, .init(reading: .failed, detail: "invalid_value"), "\(bad.debugDescription)")
            XCTAssertNil(r.value)
        }
    }

    func testEveryFailureAndUnavailabilityMapsToAClosedCode() {
        let allowed = Set(["refusal", "guardrail", "context_size", "unsupported_locale", "rate_limited", "timeout",
                           "model_not_ready", "decode", "error", "os_too_old", "device_not_eligible",
                           "apple_intelligence_not_enabled", "unavailable"])
        var replies = D.Failure.allCases.map { D.reply(failure: $0) }
        replies += [.osTooOld, .deviceNotEligible, .appleIntelligenceNotEnabled, .modelNotReady, .other].map { D.reply(unavailable: $0) }
        for r in replies {
            XCTAssertNil(r.value)
            XCTAssertNotEqual(r.reading, .answered)
            XCTAssertTrue(allowed.contains(try! XCTUnwrap(r.detail)), "\(r)")
        }
        XCTAssertEqual(D.reply(failure: .refusal).reading, .refused)
        XCTAssertEqual(D.reply(failure: .timeout).reading, .unavailable)
        XCTAssertEqual(D.reply(unavailable: .appleIntelligenceNotEnabled).detail, "apple_intelligence_not_enabled")
    }

    func testReplyEncodesOneStableObject() throws {
        let line = String(decoding: D.reply(choice: "essay", options: options).encoded(), as: UTF8.self)
        XCTAssertEqual(line, #"{"reading":"answered","v":1,"value":"essay"}"#)
        let un = String(decoding: D.reply(unavailable: .modelNotReady).encoded(), as: UTF8.self)
        XCTAssertEqual(un, #"{"detail":"model_not_ready","reading":"unavailable","v":1}"#)
        let back = try JSONDecoder().decode(D.Reply.self, from: Data(line.utf8))
        XCTAssertEqual(back, D.reply(choice: "essay", options: options))
    }

    // MARK: the built helper

    func helperBinary() throws -> URL {
        let url = Bundle(for: Self.self).bundleURL.deletingLastPathComponent().appendingPathComponent("vani-fm")
        guard FileManager.default.isExecutableFile(atPath: url.path) else {
            throw XCTSkip("vani-fm was not built next to the tests at \(url.path)")
        }
        return url
    }

    func run(_ input: Data, timeout: TimeInterval = 20) throws -> (status: Int32, out: String) {
        let p = Process()
        p.executableURL = try helperBinary()
        let inPipe = Pipe(), outPipe = Pipe()
        p.standardInput = inPipe
        p.standardOutput = outPipe
        p.standardError = FileHandle.nullDevice
        try p.run()
        // The helper may exit before reading a large request; a broken pipe is its answer, not ours.
        DispatchQueue.global().async {
            try? inPipe.fileHandleForWriting.write(contentsOf: input)
            try? inPipe.fileHandleForWriting.close()
        }
        let done = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var out = Data()
        DispatchQueue.global().async {
            out = outPipe.fileHandleForReading.readDataToEndOfFile()
            done.signal()
        }
        if done.wait(timeout: .now() + timeout) == .timedOut {
            p.terminate()
            XCTFail("vani-fm did not exit within \(timeout)s")
        }
        p.waitUntilExit()
        return (p.terminationStatus, String(decoding: out, as: UTF8.self))
    }

    func testHelperRefusesBadRequestsWithOneJSONLineAndExitZero() throws {
        let cases: [(Data, String)] = [
            (Data(), "bad_request_json"),
            (Data("not json".utf8), "bad_request_json"),
            (request { $0["task"] = "other" }, "bad_request_task"),
            (request { $0["options"] = [String]() }, "bad_request_options"),
            // The second task takes only its two fixed answers: refused before the model is asked.
            (request { $0["task"] = D.sameQuestionTask }, "bad_request_options"),
            (Data(repeating: UInt8(ascii: "a"), count: D.maxRequestBytes * 4), "bad_request_too_large"),
        ]
        for (input, detail) in cases {
            let r = try run(input)
            XCTAssertEqual(r.status, 0)
            XCTAssertEqual(r.out, #"{"detail":"\#(detail)","reading":"failed","v":1}"# + "\n")
        }
    }
}
