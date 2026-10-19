import Foundation
import XCTest
@testable import VaniApplyKit

/// A fresh directory per test, removed afterwards. Never the real App Group container.
func tempDir(_ t: XCTestCase) throws -> URL {
    let url = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-tests-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    t.addTeardownBlock { try? FileManager.default.removeItem(at: url) }
    return url
}

func answerJSON(_ key: String, kind: String = "text", value: [Any] = ["v"], labels: [Any]? = nil,
                extra: [String: Any] = [:]) -> [String: Any] {
    var a: [String: Any] = [
        "key": key, "labels": labels ?? [key], "kind": kind, "value": value, "sensitivity": "standard",
        "signatures": [], "uses": 1, "fills": 0, "updatedAt": "2026-10-18T00:00:00.000Z", "review": false,
    ]
    for (k, v) in extra { a[k] = v }
    return a
}

func bankJSON(_ answers: [[String: Any]], settings: [String: Any]? = nil, version: Any = 1) -> [String: Any] {
    var dict: [String: Any] = [:]
    for a in answers { dict[a["key"] as! String] = a }
    var b: [String: Any] = ["version": version, "answers": dict]
    if let settings { b["settings"] = settings }
    return b
}

func answer(_ key: String, _ value: String = "v") -> Answer {
    Answer(key: key, labels: [key], kind: .text, value: [value], sensitivity: .standard, signatures: [],
           uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
}
