import Foundation
import XCTest
@testable import VaniApplyKit

/// The Swift classifier against the vectors generated from the TypeScript one.
final class ClassifierVectorTests: XCTestCase {
    struct Vectors: Decodable {
        struct Norm: Decodable { let `in`: String; let want: String }
        struct Case: Decodable { let kind: String; let label: String; let name: String; let autocomplete: String; let want: String }
        let normalize: [Norm]
        let classify: [Case]
    }

    func loadVectors() throws -> Vectors {
        // Tests/VaniApplyKitTests/<file> -> macos -> Vani/extension/test/vectors
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("extension/test/vectors/classify.json")
        let data = try Data(contentsOf: url)
        return try JSONDecoder().decode(Vectors.self, from: data)
    }

    func testNormalizeMatchesTypeScript() throws {
        let v = try loadVectors()
        XCTAssertGreaterThan(v.normalize.count, 50, "vector file looks truncated")
        for n in v.normalize {
            XCTAssertEqual(Classifier.normalizeLabel(n.in), n.want, "normalizeLabel(\(String(reflecting: n.in).prefix(60)))")
        }
    }

    func testClassifyMatchesTypeScript() throws {
        let v = try loadVectors()
        XCTAssertGreaterThan(v.classify.count, 100, "vector file looks truncated")
        XCTAssertTrue(v.classify.contains { $0.want == "secret" } && v.classify.contains { $0.want == "self_id" })
        for c in v.classify {
            let kind = try XCTUnwrap(FieldKind(rawValue: c.kind))
            let got = Classifier.classify(kind: kind, label: c.label, name: c.name, autocomplete: c.autocomplete)
            XCTAssertEqual(got.rawValue, c.want, "classify(\(c.kind), \(String(reflecting: c.label).prefix(40)), name \(c.name), ac \(c.autocomplete))")
        }
    }
}
