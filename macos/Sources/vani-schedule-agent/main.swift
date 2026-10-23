import Foundation
import VaniApplyKit

// The Vani schedule agent: a login item (the app registers it with SMAppService when the
// owner turns on "Run on schedule") that keeps `vani daemon` running, the one thing that
// fires the daily run. See ScheduleAgent.swift for the design.
//
// It runs the `vani` bundled beside it with the config and state folder the app saved
// (both pinned in the child's environment), reads the decision model's key from the
// login Keychain for each start (bounded; a missing key means the model is off, and the
// daemon says so in daemon.json), and starts the daemon again, with a doubling wait up to
// five minutes, when it ends. It listens on nothing: no Mach service, no socket.
// SIGTERM (turning the switch off, logout) ends the daemon cleanly first.

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("vani-schedule-agent: \(message)\n".utf8))
    exit(1)
}

if CommandLine.arguments.dropFirst().first == "--version" {
    print("vani-schedule-agent \(ScheduleAgentConstants.label)")
    exit(0)
}

let vaniBinary = VaniProcess.helpersDirectory().appendingPathComponent("vani")
let storeDir: URL
do { storeDir = try AppGroup.storeDirectory() } catch { fail("\(error)") }

let supervisor = ScheduleSupervisor(
    attempt: { ScheduleAgent.attempt(settingsDir: storeDir, vaniBinary: vaniBinary) },
    currentFingerprint: { ScheduleAgent.currentFingerprint(settingsDir: storeDir) },
    keyExists: { Keychain.itemExists(service: Keychain.openRouterService) },
    fallbackLog: ScheduleAgent.logURL(stateDir: VaniProcess.defaultStateDir))

// SIGTERM and SIGINT end the daemon, wait for it, then exit. launchd's ExitTimeOut (60 s in
// the plist) is longer than the daemon's stop grace, so this finishes first.
let finished = DispatchSemaphore(value: 0)
nonisolated(unsafe) var signalSources: [DispatchSourceSignal] = []
for sig in [SIGTERM, SIGINT] {
    signal(sig, SIG_IGN)
    let source = DispatchSource.makeSignalSource(signal: sig, queue: .global())
    source.setEventHandler {
        supervisor.stop()
        _ = finished.wait(timeout: .now() + ScheduleAgentConstants.stopGrace + 10)
        exit(0)
    }
    source.resume()
    signalSources.append(source)
}

let runner = Thread {
    supervisor.run()
    finished.signal()
    exit(0)
}
runner.name = "vani-schedule-supervisor"
runner.start()
dispatchMain()
