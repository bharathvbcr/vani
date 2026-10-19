import Foundation
import VaniApplyKit

// The Vani Apply agent. launchd starts it on demand when a browser connects to
// its Mach service (the app registers it with SMAppService), and it exits after
// five idle minutes with no job running and no result waiting. It runs the
// `vani` binary bundled beside it with the paths and workspace the app saved,
// and reads the Anthropic key from the login Keychain per job, as the Chrome
// host does. Nothing is logged but job counts.

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("vani-apply-agent: \(message)\n".utf8))
    exit(1)
}

if CommandLine.arguments.dropFirst().first == "--version" {
    print("vani-apply-agent protocol \(NativeProtocol.version)")
    exit(0)
}

let vaniBinary = VaniProcess.helpersDirectory().appendingPathComponent("vani")
let storeDir: URL
do { storeDir = try AppGroup.storeDirectory() } catch { fail("\(error)") }

let jobs = AgentJobs { kind, request in
    // Settings are re-read per job, so a change in the app applies to the next one.
    DraftSettings.load(from: storeDir).runner(vaniBinary: vaniBinary).runJob(kind, input: request)
}

final class Service: NSObject, AgentXPC, NSXPCListenerDelegate {
    let jobs: AgentJobs
    let lastUse = NSLock()
    nonisolated(unsafe) var last = Date()

    init(jobs: AgentJobs) { self.jobs = jobs }

    func touch() { lastUse.lock(); last = Date(); lastUse.unlock() }

    func listener(_ listener: NSXPCListener, shouldAcceptNewConnection conn: NSXPCConnection) -> Bool {
        // Only code signed by this team may start jobs: anything else could
        // spend the owner's model budget.
        conn.setCodeSigningRequirement(AgentConstants.clientRequirement)
        conn.exportedInterface = NSXPCInterface(with: AgentXPC.self)
        conn.exportedObject = self
        conn.resume()
        touch()
        return true
    }

    func start(kind: String, request: Data, reply: @escaping (String?, String?) -> Void) {
        touch()
        let r = jobs.start(kind: kind, request: request)
        reply(r.id, r.error)
    }

    func result(job: String, reply: @escaping (Data?, Bool, String?) -> Void) {
        touch()
        let r = jobs.result(job: job)
        reply(r.data, r.pending, r.error)
    }

    // Safari's route to the Jobs tab's marks. `vani jobs` reads local files and spends
    // nothing, so these run at once beside a draft. Settings are re-read per call, and the
    // timeout stays under the client's XPC call timeout so a hung vani is an error, not silence.
    private var jobsOps: AgentJobsOps {
        AgentJobsOps(jobs: DraftSettings.load(from: storeDir).jobsClient(vaniBinary: vaniBinary, timeout: XPCAgentClient.callTimeout - 1))
    }

    func find(url: String, reply: @escaping (Data?, String?) -> Void) {
        touch()
        let r = jobsOps.find(url: url)
        reply(r.match, r.error)
    }

    func markApplied(key: String, reply: @escaping (String?) -> Void) {
        touch()
        reply(jobsOps.markApplied(key: key))
    }
}

let service = Service(jobs: jobs)
let listener = NSXPCListener(machServiceName: AgentConstants.machService)
listener.delegate = service
listener.resume()

let idleSeconds = 300.0
Thread.detachNewThread {
    while true {
        Thread.sleep(forTimeInterval: 15)
        service.lastUse.lock()
        let idle = Date().timeIntervalSince(service.last)
        service.lastUse.unlock()
        if idle > idleSeconds, !service.jobs.hasWork { exit(0) }
    }
}
RunLoop.main.run()
