import Foundation
import XCTest
@testable import VaniApplyKit

/// The login agent is one launchd job per user, so the last copy of the app to register it
/// owns it. A screenshot rig's copy (its `vani` a fixture that answers `jobs` only) once took
/// it over while the agent still answered, and the owner's app kept it: both browsers then
/// ran the fixture. These pin that an answering agent from another copy is replaced.
final class AgentIdentityTests: XCTestCase {
    let own = URL(fileURLWithPath: "/Users/someone/Code/apps/Vani/macos/build/Vani Apply.app")

    func testTheBundleIsTheAppAroundTheHelpersFolder() {
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: URL(fileURLWithPath: "/Applications/Vani Apply.app/Contents/Helpers")),
                       "/Applications/Vani Apply.app")
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: URL(fileURLWithPath: "/Applications/Vani Apply.app/Contents/Helpers/")),
                       "/Applications/Vani Apply.app")
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: URL(fileURLWithPath: "/Applications/x/../Vani Apply.app/Contents/Helpers")),
                       "/Applications/Vani Apply.app")
        // Not inside a bundle (a `swift build` product, or a look-alike folder): the folder itself.
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: URL(fileURLWithPath: "/opt/build/debug")), "/opt/build/debug")
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: URL(fileURLWithPath: "/opt/NotAnApp/Contents/Helpers")),
                       "/opt/NotAnApp/Contents/Helpers")
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: URL(fileURLWithPath: "/opt/X.app/Resources/Helpers")),
                       "/opt/X.app/Resources/Helpers")
    }

    func testTheBundleResolvesSymlinks() throws {
        let dir = try tempDir(self)
        let helpers = dir.appendingPathComponent("Real.app/Contents/Helpers")
        try FileManager.default.createDirectory(at: helpers, withIntermediateDirectories: true)
        let link = dir.appendingPathComponent("Link.app")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: dir.appendingPathComponent("Real.app"))
        let real = AgentIdentity.bundlePath(helpers: helpers)
        XCTAssertTrue(real.hasSuffix("/Real.app"), real)
        XCTAssertEqual(AgentIdentity.bundlePath(helpers: link.appendingPathComponent("Contents/Helpers")), real)
        XCTAssertNil(AgentIdentity.reregister(repair: false, agentBundle: real, ownBundle: link), "one bundle by two names is still this app")
    }

    func testAnAgentFromAnotherCopyIsReplaced() {
        let rig = "/private/tmp/vani-rank-pkg/macos/build/Vani Apply.app"
        XCTAssertEqual(AgentIdentity.reregister(repair: false, agentBundle: rig, ownBundle: own), .foreign(rig))
        XCTAssertEqual(AgentIdentity.reregister(repair: false, agentBundle: "/Applications/Vani Apply.app", ownBundle: own),
                       .foreign("/Applications/Vani Apply.app"))
        // A look-alike path is not this bundle.
        XCTAssertEqual(AgentIdentity.reregister(repair: false, agentBundle: own.path + "x", ownBundle: own), .foreign(own.path + "x"))
        XCTAssertEqual(AgentIdentity.reregister(repair: false, agentBundle: "/", ownBundle: own), .foreign("/"))
    }

    func testOnlyThisAppsOwnAnsweringAgentIsKept() {
        XCTAssertNil(AgentIdentity.reregister(repair: false, agentBundle: own.path, ownBundle: own))
        XCTAssertNil(AgentIdentity.reregister(repair: false, agentBundle: own.path + "/", ownBundle: own))
        XCTAssertNil(AgentIdentity.reregister(repair: false, agentBundle: own.deletingLastPathComponent().path + "/./Vani Apply.app", ownBundle: own))
        // Not answering (or an agent built before `identity`) reads as stale, never as this app's.
        XCTAssertEqual(AgentIdentity.reregister(repair: false, agentBundle: nil, ownBundle: own), .unreachable)
        XCTAssertEqual(AgentIdentity.reregister(repair: false, agentBundle: "", ownBundle: own), .foreign(""))
        // Register again always replaces it, even this app's own.
        XCTAssertEqual(AgentIdentity.reregister(repair: true, agentBundle: own.path, ownBundle: own), .repair)
        XCTAssertEqual(AgentIdentity.reregister(repair: true, agentBundle: nil, ownBundle: own), .repair)
    }

    func testAForeignPathIsBoundedForDisplay() {
        let long = "/" + String(repeating: "a", count: 10_000)
        guard case let .foreign(shown)? = AgentIdentity.reregister(repair: false, agentBundle: long, ownBundle: own) else {
            return XCTFail("a long foreign path is still foreign")
        }
        XCTAssertEqual(shown.count, 512)
    }

    func testAnUnreachableAgentHasNoIdentityAndIsNotWaitedOut() {
        let client = XPCAgentClient(serviceName: "\(AppGroup.identifier).no-such-service-\(UUID().uuidString)")
        let started = Date()
        XCTAssertNil(client.identity())
        XCTAssertLessThan(Date().timeIntervalSince(started), XPCAgentClient.callTimeout + 1)
    }

    func testIdentityCrossesAnXPCConnection() throws {
        let service = JobAppliedTests.Service(ops: AgentJobsOps(jobs: JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))))
        let listener = NSXPCListener.anonymous()
        listener.delegate = service
        listener.resume()
        let conn = NSXPCConnection(listenerEndpoint: listener.endpoint)
        conn.remoteObjectInterface = NSXPCInterface(with: AgentXPC.self)
        conn.resume()
        defer { conn.invalidate(); listener.invalidate() }
        let proxy = try XCTUnwrap(conn.synchronousRemoteObjectProxyWithErrorHandler { XCTFail("\($0)") } as? AgentXPC)
        var path: String?
        proxy.identity { path = $0 }
        XCTAssertEqual(path, "/Applications/Vani Apply.app")
    }

    /// An agent from before `identity` (the interface it exported then) fails the call
    /// instead of answering, which XPCAgentClient.identity reads as not answering.
    @objc protocol OldAgentXPC {
        func result(job: String, reply: @escaping (Data?, Bool, String?) -> Void)
    }

    final class OldAgent: NSObject, OldAgentXPC, NSXPCListenerDelegate {
        func listener(_ listener: NSXPCListener, shouldAcceptNewConnection c: NSXPCConnection) -> Bool {
            c.exportedInterface = NSXPCInterface(with: OldAgentXPC.self)
            c.exportedObject = self
            c.resume()
            return true
        }
        func result(job: String, reply: @escaping (Data?, Bool, String?) -> Void) { reply(nil, false, "no such job") }
    }

    func testAnAgentFromBeforeIdentityFailsTheCall() throws {
        let service = OldAgent()
        let listener = NSXPCListener.anonymous()
        listener.delegate = service
        listener.resume()
        let conn = NSXPCConnection(listenerEndpoint: listener.endpoint)
        conn.remoteObjectInterface = NSXPCInterface(with: AgentXPC.self)
        conn.resume()
        defer { conn.invalidate(); listener.invalidate() }
        var failed = false, path: String?
        let proxy = try XCTUnwrap(conn.synchronousRemoteObjectProxyWithErrorHandler { _ in failed = true } as? AgentXPC)
        proxy.identity { path = $0 }
        XCTAssertNil(path)
        XCTAssertTrue(failed, "the old agent must fail the call, not answer it")
    }
}
