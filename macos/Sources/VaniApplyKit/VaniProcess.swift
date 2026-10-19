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
    }

    /// The environment every `vani` child gets: a fixed PATH, the user's home,
    /// and Vani's config and state locations when set (empty means Vani's default).
    public static func environment(configPath: String?, stateDir: String?) -> [String: String] {
        var env: [String: String] = [
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
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
        if let inPipe, let input {
            inPipe.fileHandleForWriting.write(input)
            try? inPipe.fileHandleForWriting.close()
        }

        let done = DispatchSemaphore(value: 0)
        let box = OutputBox()
        DispatchQueue.global().async {
            box.out = outPipe.fileHandleForReading.readData(upTo: maxOutputBytes)
            _ = errPipe.fileHandleForReading.readData(upTo: 64 * 1024)
            done.signal()
        }
        if done.wait(timeout: .now() + timeout) == .timedOut {
            p.terminate()
            _ = done.wait(timeout: .now() + 2)
            return Outcome(status: nil, stdout: Data(), timedOut: true, launchError: nil)
        }
        p.waitUntilExit()
        return Outcome(status: p.terminationStatus, stdout: box.out, timedOut: false, launchError: nil,
                       truncated: box.out.count >= maxOutputBytes)
    }
}

private final class OutputBox: @unchecked Sendable {
    var out = Data()
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
