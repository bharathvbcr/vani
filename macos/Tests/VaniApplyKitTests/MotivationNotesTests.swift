import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani config motivation`: records its arguments, config path
/// and stdin beside itself, then replies with `reply` and exits `status`.
private func fakeNotesVani(_ t: XCTestCase, reply: String, status: Int32 = 0) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    d=$(dirname "$0")
    printf '%s\\n' "$*" > "$d/args"
    printf '%s' "${VANI_CONFIG:-none}" > "$d/config"
    cat > "$d/stdin"
    cat <<'EOF'
    \(reply)
    EOF
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private func recorded(_ vani: URL, _ name: String) throws -> String {
    try String(contentsOf: vani.deletingLastPathComponent().appendingPathComponent(name), encoding: .utf8)
}

final class MotivationNotesTests: XCTestCase {
    func testLoadReadsTheConfigDraftingUses() throws {
        let vani = try fakeNotesVani(self, reply: #"{"motivation_notes":"I care about safe AI.","max_bytes":2048}"#)
        let settings = DraftSettings(vaniConfig: "/tmp/vani-test/config.json")
        let notes = try settings.motivationNotesClient(vaniBinary: vani).load()
        XCTAssertEqual(notes, MotivationNotes(text: "I care about safe AI.", maxBytes: 2048))
        XCTAssertEqual(try recorded(vani, "args"), "config motivation\n")
        XCTAssertEqual(try recorded(vani, "config"), "/tmp/vani-test/config.json")
        XCTAssertEqual(try recorded(vani, "stdin"), "", "a read sends nothing")
    }

    func testSaveSendsTheNoteOnStdinNeverAsAnArgument() throws {
        let vani = try fakeNotesVani(self, reply: #"{"motivation_notes":"New note","max_bytes":2048}"#)
        let note = "New note --set; $(echo injected)"
        let saved = try MotivationNotesClient(vaniBinary: vani).save(note)
        XCTAssertEqual(saved.text, "New note")
        XCTAssertEqual(try recorded(vani, "args"), "config motivation --set\n")
        XCTAssertEqual(try recorded(vani, "stdin"), note)
        XCTAssertEqual(try recorded(vani, "config"), "none", "an empty config path means Vani's default")
    }

    func testVaniRefusalIsShownAsItsError() throws {
        let vani = try fakeNotesVani(self, reply: #"{"motivation_notes":"","max_bytes":2048,"error":"motivation_notes is 3000 bytes after whitespace collapse, max 2048"}"#, status: 1)
        XCTAssertThrowsError(try MotivationNotesClient(vaniBinary: vani).save("x")) {
            XCTAssertTrue(String(describing: $0).contains("motivation_notes is 3000 bytes"), "\($0)")
        }
    }

    func testOldVaniIsNamed() throws {
        let vani = try fakeNotesVani(self, reply: "", status: 2)
        XCTAssertThrowsError(try MotivationNotesClient(vaniBinary: vani).load()) {
            XCTAssertTrue(String(describing: $0).contains("too old for motivation notes"), "\($0)")
            XCTAssertTrue(String(describing: $0).contains("vani config motivation"), "\($0)")
        }
    }
}
