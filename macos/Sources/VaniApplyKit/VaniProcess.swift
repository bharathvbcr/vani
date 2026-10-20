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

extension VaniProcess {
    private struct Failure: Decodable { var error: String }

    /// Runs a `vani` command that writes one JSON reply on stdout: the value on
    /// exit 0, or `{"error": ...}` on failure, which is thrown as is. stderr is
    /// never shown. `tooOld` explains an exit 2 (a usage error: this vani
    /// predates the command).
    static func call<T: Decodable>(_ binary: URL, _ args: [String], configPath: String?, stateDir: String?,
                                   input: Data? = nil, timeout: TimeInterval, maxOutputBytes: Int,
                                   decoder: JSONDecoder = JSONDecoder(), tooOld: String) throws -> T {
        let (out, name) = try finished(binary, args, configPath: configPath, stateDir: stateDir, input: input,
                                       timeout: timeout, maxOutputBytes: maxOutputBytes)
        if out.status == 0, let value = try? decoder.decode(T.self, from: out.stdout) {
            return value
        }
        throw failure(out, name: name, decoder: decoder, tooOld: tooOld)
    }

    /// Like `call`, but the reply is handed back as the bytes vani wrote, unchanged:
    /// on exit 0 they must be one JSON object. For a caller that relays the reply as is.
    static func callRaw(_ binary: URL, _ args: [String], configPath: String?, stateDir: String?,
                        input: Data? = nil, timeout: TimeInterval, maxOutputBytes: Int, tooOld: String) throws -> Data {
        let (out, name) = try finished(binary, args, configPath: configPath, stateDir: stateDir, input: input,
                                       timeout: timeout, maxOutputBytes: maxOutputBytes)
        if out.status == 0, (try? JSONSerialization.jsonObject(with: out.stdout)) is [String: Any] {
            return out.stdout
        }
        throw failure(out, name: name, decoder: JSONDecoder(), tooOld: tooOld)
    }

    /// Runs the command and refuses what no reply can come from: it did not start, did
    /// not finish in time, or wrote more than the ceiling. Also returns the command's
    /// name: its leading words, before any flag.
    private static func finished(_ binary: URL, _ args: [String], configPath: String?, stateDir: String?,
                                 input: Data?, timeout: TimeInterval, maxOutputBytes: Int) throws -> (Outcome, String) {
        let out = run(binary, arguments: args, environment: environment(configPath: configPath, stateDir: stateDir),
                      input: input, timeout: timeout, maxOutputBytes: maxOutputBytes)
        let name = "vani \(args.prefix { !$0.hasPrefix("-") }.prefix(2).joined(separator: " "))"
        if let launchError = out.launchError { throw BankError(launchError) }
        if out.timedOut { throw BankError("\(name) did not finish within \(Int(timeout)) s") }
        let cap = maxOutputBytes >= 1 << 20 ? "\(maxOutputBytes >> 20) MB" : "\(maxOutputBytes >> 10) KB"
        if out.truncated { throw BankError("vani's reply was larger than \(cap)") }
        return (out, name)
    }

    /// vani's `{"error": ...}` as it wrote it, or an account of the missing reply.
    private static func failure(_ out: Outcome, name: String, decoder: JSONDecoder, tooOld: String) -> BankError {
        if let failure = try? decoder.decode(Failure.self, from: out.stdout) {
            return BankError(failure.error)
        }
        return BankError("\(name) exited \(out.status ?? -1) without a reply" + (out.status == 2 ? " (\(tooOld))" : ""))
    }
}

extension VaniProcess {
    /// Start a long `vani` job with no timeout: stdin is /dev/null, and stdout
    /// and stderr go to `log` (created 0600 and truncated; a symlink is refused),
    /// never to a pipe, so the job runs on and keeps its output if this app
    /// quits. `onExit` gets the exit status when the job ends while this process
    /// still runs.
    public static func launch(_ program: URL, arguments: [String], environment: [String: String], log: URL,
                              onExit: @escaping @Sendable (Int32) -> Void) throws -> LaunchedJob {
        guard FileManager.default.isExecutableFile(atPath: program.path) else {
            throw BankError("the vani binary is missing at \(program.path)")
        }
        try FileManager.default.createDirectory(at: log.deletingLastPathComponent(), withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        let fd = open(log.path, O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard fd >= 0 else { throw BankError("cannot open the run log \(log.path): \(String(cString: strerror(errno)))") }
        let out = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
        defer { try? out.close() } // the child has its own copy once it runs
        let p = Process()
        p.executableURL = program
        p.arguments = arguments
        p.environment = environment
        p.standardInput = FileHandle.nullDevice
        p.standardOutput = out
        p.standardError = out
        p.terminationHandler = { onExit($0.terminationStatus) }
        do { try p.run() } catch {
            throw BankError("could not start vani: \(error.localizedDescription)")
        }
        return LaunchedJob(process: p, log: log)
    }

    /// The last line `vani` wrote about itself in a job's log ("vani: …", the
    /// CLI's own error and warning prefix), bounded to one line; nil when the
    /// log is unreadable or has none. Only the log's last 16 KB are read.
    public static func lastVaniLine(inLog log: URL, maxCharacters: Int = 300) -> String? {
        guard let h = try? FileHandle(forReadingFrom: log) else { return nil }
        defer { try? h.close() }
        let size = (try? h.seekToEnd()) ?? 0
        try? h.seek(toOffset: size > 16_384 ? size - 16_384 : 0)
        guard let data = try? h.readToEnd(), let text = String(data: data, encoding: .utf8) else { return nil }
        let line = text.split(whereSeparator: \.isNewline).last { $0.hasPrefix("vani: ") }
        return line.map { PipelineStatus.bounded(String($0.dropFirst("vani: ".count)), max: maxCharacters) }
    }
}

/// A `vani` job started by `VaniProcess.launch`.
public final class LaunchedJob: @unchecked Sendable {
    private let process: Process
    public let log: URL
    public let pid: Int32
    public let startedAt: Date

    init(process: Process, log: URL, startedAt: Date = Date()) {
        self.process = process
        self.log = log
        pid = process.processIdentifier
        self.startedAt = startedAt
    }

    public var isRunning: Bool { process.isRunning }

    /// The exit status once the job has ended; nil while it runs.
    public var exitStatus: Int32? { process.isRunning ? nil : process.terminationStatus }

    /// SIGTERM: vani cancels the run and records it as cancelled.
    public func stop() {
        if process.isRunning { process.terminate() }
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
