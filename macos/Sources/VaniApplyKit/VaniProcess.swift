import Foundation

/// Runs Vani's Go CLI as a child process: a minimal environment (never this
/// process's), optional stdin, a ceiling on time and on bytes kept from stdout.
/// The one place the app and the Chrome host start `vani`; `vani draft` and
/// `vani jobs` both go through it.
public enum VaniProcess {
    /// The folder holding this process's executable (the app's Contents/Helpers
    /// for the host and the agent), where the bundled `vani` lives. It never
    /// trusts argv[0]: launchd starts an SMAppService agent with argv[0] set to
    /// the plist's relative BundleProgram, which would resolve against "/".
    public static func helpersDirectory(argv0: String = CommandLine.arguments.first ?? "",
                                        executable: URL? = Bundle.main.executableURL) -> URL {
        if let executable, executable.path.hasPrefix("/") {
            return executable.resolvingSymlinksInPath().deletingLastPathComponent()
        }
        return URL(fileURLWithPath: argv0).resolvingSymlinksInPath().deletingLastPathComponent()
    }

    public struct Outcome: Sendable {
        /// The exit status, or nil when the process could not start or timed out.
        public var status: Int32?
        public var stdout: Data
        public var timedOut: Bool
        /// Why the process could not start, when it could not.
        public var launchError: String?
        /// stdout reached the byte ceiling, so it may be cut short.
        public var truncated = false
        /// The first 64 KB of stderr. Never put in a reply as is: a caller
        /// picks out only lines it knows carry no secret.
        public var stderr = Data()
    }

    /// Vani's own default config file (config.Paths in the Go CLI), used when
    /// no VANI_CONFIG is set.
    public static var defaultConfigPath: String {
        NSHomeDirectory() + "/Library/Application Support/Vani/config.json"
    }

    /// The environment every `vani` child gets: a fixed PATH, the user's home,
    /// and Vani's config and state locations when set (empty means Vani's default).
    /// The Homebrew prefixes (Apple silicon, then Intel) come after the system
    /// folders, so a tool installed there never shadows a system one.
    public static func environment(configPath: String?, stateDir: String?) -> [String: String] {
        var env: [String: String] = [
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin",
            "HOME": NSHomeDirectory(),
            "USER": NSUserName(),
            "TMPDIR": NSTemporaryDirectory(),
        ]
        if let configPath, !configPath.isEmpty { env["VANI_CONFIG"] = configPath }
        if let stateDir, !stateDir.isEmpty { env["VANI_STATE_DIR"] = stateDir }
        return env
    }

    public static func run(_ binary: URL, arguments: [String], environment: [String: String],
                           input: Data? = nil, timeout: TimeInterval, maxOutputBytes: Int) -> Outcome {
        guard FileManager.default.isExecutableFile(atPath: binary.path) else {
            return Outcome(status: nil, stdout: Data(), timedOut: false, launchError: "the vani binary is missing at \(binary.path)")
        }
        let p = Process()
        p.executableURL = binary
        p.arguments = arguments
        p.environment = environment
        let outPipe = Pipe(), errPipe = Pipe()
        let inPipe = input.map { _ in Pipe() }
        p.standardInput = inPipe ?? FileHandle.nullDevice
        p.standardOutput = outPipe
        p.standardError = errPipe
        do { try p.run() } catch {
            return Outcome(status: nil, stdout: Data(), timedOut: false, launchError: "could not start vani: \(error.localizedDescription)")
        }
        // stdin, stdout and stderr each move on their own thread: any one of
        // them done in sequence before another lets a child that fills one pipe
        // while the parent waits on a different one stall until the timeout.
        let done = DispatchGroup()
        let box = OutputBox()
        if let inPipe, let input {
            let writer = inPipe.fileHandleForWriting
            // A child that exits without reading its input must not raise
            // SIGPIPE in this process; the write then fails with EPIPE instead.
            _ = fcntl(writer.fileDescriptor, F_SETNOSIGPIPE, 1)
            DispatchQueue.global().async(group: done) {
                try? writer.write(contentsOf: input)
                try? writer.close()
            }
        }
        DispatchQueue.global().async(group: done) {
            box.out = outPipe.fileHandleForReading.readData(upTo: maxOutputBytes)
        }
        DispatchQueue.global().async(group: done) {
            box.err = errPipe.fileHandleForReading.readData(upTo: 64 * 1024)
        }
        if done.wait(timeout: .now() + timeout) == .timedOut {
            p.terminate()
            _ = done.wait(timeout: .now() + 2)
            return Outcome(status: nil, stdout: Data(), timedOut: true, launchError: nil)
        }
        p.waitUntilExit()
        return Outcome(status: p.terminationStatus, stdout: box.out, timedOut: false, launchError: nil,
                       truncated: box.out.count >= maxOutputBytes, stderr: box.err)
    }
}

/// Each field is written by one reader thread and read only after the group
/// has finished.
private final class OutputBox: @unchecked Sendable {
    var out = Data()
    var err = Data()
}

extension FileHandle {
    /// Read to end of file, keeping at most `limit` bytes (the rest is drained and dropped).
    func readData(upTo limit: Int) -> Data {
        var out = Data()
        while true {
            guard let chunk = try? read(upToCount: 64 * 1024), !chunk.isEmpty else { break }
            if out.count < limit { out.append(chunk.prefix(limit - out.count)) }
        }
        return out
    }
}
