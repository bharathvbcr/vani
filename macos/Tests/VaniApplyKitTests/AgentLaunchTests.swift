import XCTest
@testable import VaniApplyKit

/// -VaniApplyNoAgent: a rig leaves the shared login agent alone only when it
/// says YES plainly; anything else registers, and a near miss is reported.
final class AgentLaunchTests: XCTestCase {
    private let exe = "/Applications/Vani Apply.app/Contents/MacOS/VaniApply"

    private func read(_ rest: [String]) -> (mode: AgentLaunch, problem: String?) {
        AgentLaunch.from(arguments: [exe] + rest)
    }

    func testAbsentRegistersSilently() {
        XCTAssertEqual(read([]).mode, .register)
        XCTAssertNil(read([]).problem)
        // macOS adds its own launch arguments; they are not the switch.
        let finder = read(["-NSDocumentRevisionsDebugMode", "YES", "-ApplePersistenceIgnoreState", "YES"])
        XCTAssertEqual(finder.mode, .register)
        XCTAssertNil(finder.problem)
        XCTAssertEqual(AgentLaunch.from(arguments: []).mode, .register)
    }

    func testTruthyLeavesTheAgentAlone() {
        for v in ["YES", "yes", "Yes", "TRUE", "true", "1", " YES "] {
            let r = read([AgentLaunch.argument, v])
            XCTAssertEqual(r.mode, .leaveAlone, v)
            XCTAssertNil(r.problem, v)
        }
    }

    func testFalsyRegistersSilently() {
        for v in ["NO", "no", "FALSE", "false", "0"] {
            let r = read([AgentLaunch.argument, v])
            XCTAssertEqual(r.mode, .register, v)
            XCTAssertNil(r.problem, v)
        }
    }

    func testEmptyOrGarbageRegistersAndSaysWhy() {
        for v in ["", " ", "maybe", "YESS", "2", "on", "y", "\u{0}", "YES\nNO"] {
            let r = read([AgentLaunch.argument, v])
            XCTAssertEqual(r.mode, .register, v.debugDescription)
            XCTAssertNotNil(r.problem, v.debugDescription)
        }
    }

    func testMissingValueRegistersAndSaysWhy() {
        let last = read([AgentLaunch.argument])
        XCTAssertEqual(last.mode, .register)
        XCTAssertNotNil(last.problem)
        // The next token is another flag, not a value.
        let flag = read([AgentLaunch.argument, "-NSDocumentRevisionsDebugMode", "YES"])
        XCTAssertEqual(flag.mode, .register)
        XCTAssertNotNil(flag.problem)
    }

    func testLastOccurrenceWins() {
        XCTAssertEqual(read([AgentLaunch.argument, "NO", AgentLaunch.argument, "YES"]).mode, .leaveAlone)
        XCTAssertEqual(read([AgentLaunch.argument, "YES", AgentLaunch.argument, "NO"]).mode, .register)
        XCTAssertEqual(read([AgentLaunch.argument, "YES", AgentLaunch.argument]).mode, .register)
    }

    func testNearMissesRegisterAndSayWhy() {
        for a in ["-VaniApplyNoAgent=YES", "-vaniapplynoagent", "-VANIAPPLYNOAGENT"] {
            let r = read([a, "YES"])
            XCTAssertEqual(r.mode, .register, a)
            XCTAssertNotNil(r.problem, a)
        }
        // Argument 0 is the executable, never the switch.
        XCTAssertEqual(AgentLaunch.from(arguments: [AgentLaunch.argument, "YES"]).mode, .register)
        XCTAssertNil(AgentLaunch.from(arguments: [AgentLaunch.argument, "YES"]).problem)
        XCTAssertEqual(AgentLaunch.from(arguments: [AgentLaunch.argument]).mode, .register)
    }

    func testOversizedValueIsCappedInTheProblem() {
        let r = read([AgentLaunch.argument, String(repeating: "x", count: 100_000)])
        XCTAssertEqual(r.mode, .register)
        let problem = try? XCTUnwrap(r.problem)
        XCTAssertLessThan(problem?.count ?? .max, 200)
    }
}
