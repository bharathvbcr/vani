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

    /// How long a child gets to act on SIGTERM before SIGKILL, and again to be reaped after it.
    public static let terminationGrace: TimeInterval = 1
    /// How often a reader or writer thread looks at its cancel flag while it waits on a pipe.
    private static let pollMilliseconds: Int32 = 100

    /// Runs `binary` and returns when it has exited and both of its pipes have ended, or at
    /// `timeout` seconds. A child still running then gets SIGTERM, and SIGKILL after
    /// `terminationGrace` if it ignores that, and the call reports `timedOut` however the child
    /// behaved (closed its pipes and sat, ignored TERM, left a grandchild holding the pipes). The
    /// I/O threads always end with the call: they poll their pipes and stop when told to.
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
        let exited = DispatchSemaphore(value: 0)
        p.terminationHandler = { _ in exited.signal() }
        let deadline = DispatchTime.now() + timeout
        do { try p.run() } catch {
            return Outcome(status: nil, stdout: Data(), timedOut: false, launchError: "could not start vani: \(error.localizedDescription)")
        }
        // stdin, stdout and stderr each move on their own thread: any one of
        // them done in sequence before another lets a child that fills one pipe
        // while the parent waits on a different one stall until the timeout.
        let done = DispatchGroup()
        let box = OutputBox()
        let cancel = CancelFlag()
        if let inPipe, let input {
            let writer = inPipe.fileHandleForWriting
            let fd = writer.fileDescriptor
            // A child that exits without reading its input must not raise
            // SIGPIPE in this process; the write then fails with EPIPE instead.
            _ = fcntl(fd, F_SETNOSIGPIPE, 1)
            _ = fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK)
            spawn(done) {
                feed(input, to: fd, cancel: cancel)
                try? writer.close()
            }
        }
        let outFd = outPipe.fileHandleForReading.fileDescriptor, errFd = errPipe.fileHandleForReading.fileDescriptor
        // One byte past the ceiling is read, so a reply of exactly the ceiling is told from a longer one.
        spawn(done) { box.out = drain(outFd, limit: maxOutputBytes + 1, cancel: cancel) }
        spawn(done) { box.err = drain(errFd, limit: 64 * 1024, cancel: cancel) }
        defer {
            // Whatever happened, no thread is left on these descriptors when they are closed.
            cancel.set()
            _ = done.wait(timeout: .now() + terminationGrace)
            try? outPipe.fileHandleForReading.close()
            try? errPipe.fileHandleForReading.close()
        }
        if done.wait(timeout: deadline) == .timedOut || exited.wait(timeout: deadline) == .timedOut {
            stop(p, exited: exited)
            return Outcome(status: nil, stdout: Data(), timedOut: true, launchError: nil)
        }
        let truncated = box.out.count > maxOutputBytes
        return Outcome(status: p.terminationStatus, stdout: truncated ? Data(box.out.prefix(maxOutputBytes)) : box.out,
                       timedOut: false, launchError: nil, truncated: truncated, stderr: box.err)
    }

    /// SIGTERM, then SIGKILL after the grace period, then a last short wait for the exit to be
    /// reaped. Foundation reaps the child before it reports it no longer running, so a pid is
    /// signalled only while `isRunning` still says this process owns it: a reused pid is never hit.
    private static func stop(_ p: Process, exited: DispatchSemaphore) {
        if p.isRunning { p.terminate() }
        guard exited.wait(timeout: .now() + terminationGrace) == .timedOut else { return }
        if p.isRunning { kill(p.processIdentifier, SIGKILL) }
        _ = exited.wait(timeout: .now() + terminationGrace)
    }

    /// One short-lived thread per pipe. Not the dispatch pool: a call can sit here for as long as
    /// its timeout, and parked pool threads are what starves everything else.
    private static func spawn(_ group: DispatchGroup, _ body: @escaping @Sendable () -> Void) {
        group.enter()
        let thread = Thread {
            body()
            group.leave()
        }
        thread.stackSize = 256 * 1024
        thread.start()
    }

    /// Read `fd` to end of file, keeping at most `limit` bytes (the rest is drained and dropped),
    /// or until `cancel` is set. It waits in poll(2), so it notices `cancel` within `pollMilliseconds`
    /// even when a grandchild keeps the pipe open and silent.
    private static func drain(_ fd: Int32, limit: Int, cancel: CancelFlag) -> Data {
        var out = Data()
        var chunk = [UInt8](repeating: 0, count: 64 * 1024)
        while !cancel.isSet {
            var p = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
            let ready = poll(&p, 1, pollMilliseconds)
            if ready < 0 { if errno == EINTR { continue } else { break } }
            if ready == 0 { continue }
            let n = read(fd, &chunk, chunk.count)
            if n < 0 { if errno == EINTR || errno == EAGAIN { continue } else { break } }
            if n == 0 { break }
            if out.count < limit { out.append(contentsOf: chunk.prefix(min(n, limit - out.count))) }
        }
        return out
    }

    /// Write all of `data` to the non-blocking `fd`, giving up on any error (EPIPE: the child
    /// is gone) or when `cancel` is set.
    private static func feed(_ data: Data, to fd: Int32, cancel: CancelFlag) {
        var offset = 0
        while offset < data.count, !cancel.isSet {
            let n = data.withUnsafeBytes { write(fd, $0.baseAddress! + offset, data.count - offset) }
            if n >= 0 { offset += n; continue }
            if errno == EINTR { continue }
            guard errno == EAGAIN else { return }
            var p = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
            _ = poll(&p, 1, pollMilliseconds)
        }
    }
}

/// A reply type that carries the contract version vani wrote it under (PipelineStatus).
protocol VersionedReply {
    static var supportedVersion: Int { get }
}

extension PipelineStatus: VersionedReply {}

private struct VersionProbe: Decodable { var version: Int? }

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
        guard out.status == 0 else { throw failure(out, name: name, decoder: decoder, tooOld: tooOld) }
        // A reply of another contract version is named as that before anything else is read from it:
        // its shape is not this app's to judge.
        if let versioned = T.self as? VersionedReply.Type {
            do {
                if let seen = try decoder.decode(VersionProbe.self, from: out.stdout).version, seen != versioned.supportedVersion {
                    throw BankError("\(name) reports reply version \(seen); this app reads version \(versioned.supportedVersion). Rebuild the app.")
                }
            } catch is DecodingError {
                // JSON whose version this app cannot hold (1.5, a string, 10^23) is a version problem,
                // named without the value; only non-JSON is "without a reply".
                if let object = (try? JSONSerialization.jsonObject(with: out.stdout)) as? [String: Any],
                   let raw = object["version"], !(raw is NSNull) {
                    throw BankError("\(name) sent a reply this app cannot read: version is not an integer this app can hold. Rebuild the app.")
                }
            }
        }
        do {
            return try decoder.decode(T.self, from: out.stdout)
        } catch let error as DecodingError {
            if let reported = try? decoder.decode(Failure.self, from: out.stdout) { throw BankError(reported.error) }
            if let place = describe(error) { throw BankError("\(name) sent a reply this app cannot read: \(place)") }
            throw failure(out, name: name, decoder: decoder, tooOld: tooOld)
        } catch {
            throw failure(out, name: name, decoder: decoder, tooOld: tooOld)
        }
    }

    /// Where a reply of the wrong shape went wrong, as a path of keys and positions and the kind
    /// of mismatch, never the value found there (it may be a secret). Nil for text that is not
    /// JSON at all, which has no path to give.
    static func describe(_ error: DecodingError) -> String? {
        let context: DecodingError.Context, what: String
        switch error {
        case let .typeMismatch(_, c): (context, what) = (c, "has the wrong type")
        case let .valueNotFound(_, c): (context, what) = (c, "is missing a value")
        case let .keyNotFound(key, c):
            return "\(path(c.codingPath + [key])) is missing"
        default: return nil
        }
        return "\(path(context.codingPath)) \(what)"
    }

    private static func path(_ keys: [CodingKey]) -> String {
        guard !keys.isEmpty else { return "the reply" }
        return keys.reduce(into: "") { out, key in
            if let index = key.intValue { out += "[\(index)]" } else { out += (out.isEmpty ? "" : ".") + key.stringValue }
        }
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

/// Set once to tell a pipe thread to stop.
private final class CancelFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    var isSet: Bool { lock.lock(); defer { lock.unlock() }; return value }
    func set() { lock.lock(); value = true; lock.unlock() }
}
