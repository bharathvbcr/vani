import Foundation

/// `vani jobs serve`, kept running for the Jobs tab: one process per vani binary, config and state
/// folder, answering `jobs list|stats|gaps|show` over NDJSON on its stdin and stdout
/// (`cmd/vani/jobsserve.go`). Its result for a request is byte for byte the reply
/// `vani jobs <op> --json` prints, so a reply decodes exactly as the command's does.
///
/// It only ever makes the tab faster. It starts in the background on the first request, which,
/// like every request until serve has said hello, runs the command as before. Whenever serve
/// cannot answer a request (it is starting, it died, it speaks another protocol, it was busy, or
/// it refused the request as malformed), `ask` returns nil and the caller runs the command itself.
/// A request the command itself failed is thrown as the command's own error, one serve replaced
/// with a newer request as `VaniCallCancelled`, and one serve gave up on at its deadline as the
/// command's timeout, so falling back never doubles a slow wait.
public final class JobsServeClient: @unchecked Sendable {
    /// The ops serve answers; anything else always runs the command.
    public static let ops: Set<String> = ["list", "stats", "gaps", "show"]
    /// The wire's major version (`jobsServeProtocol`).
    public static let protocolVersion = 1
    /// How long serve may take to say hello (it opens the ledger checkpoint and the facts store
    /// first) before this start is given up.
    static let helloTimeout: TimeInterval = 60
    /// How long after serve died a request waits before starting it again.
    static let restartBackoff: TimeInterval = 2
    /// Starts allowed per client: the first and one restart. Past them every request runs the
    /// command, and `unavailable` says why.
    static let maxStarts = 2
    /// The longest response line read (a 5,000-job list is about 5 MB; JobsClient caps replies at 16 MB).
    static let maxLineBytes = 32 << 20

    /// What serve answered for a request: the command's reply bytes, or the command's failure.
    public enum Answer: Equatable {
        case reply(Data)
        case failed(message: String)
    }

    let binary: URL
    let environment: [String: String]

    private let lock = NSLock()
    /// Keeps each request line whole on serve's stdin; never held with `lock`.
    private let writeLock = NSLock()
    /// The process, from its spawn until it dies or is stopped.
    private var running: Running?
    /// Whether `running` has said hello; requests go to it only then.
    private var ready = false
    private var pending: [String: Waiter] = [:]
    private var nextID = 0
    private var starts = 0
    private var diedAt: Date?
    private var disabled: String?
    private var answeredCount = 0

    /// One running `vani jobs serve`.
    private final class Running: @unchecked Sendable {
        let process: Process
        /// The write end of serve's stdin; closing it is how serve is asked to exit.
        let stdin: FileHandle
        let generation: Int
        var input: Int32 { stdin.fileDescriptor }
        init(process: Process, stdin: FileHandle, generation: Int) {
            self.process = process
            self.stdin = stdin
            self.generation = generation
        }
        func closeInput() { try? stdin.close() }
    }

    /// One request waiting for its line.
    private final class Waiter: @unchecked Sendable {
        let done = DispatchSemaphore(value: 0)
        var line: Data? // nil with done signalled: serve died or was stopped
    }

    public init(binary: URL, configPath: String?, stateDir: String?) {
        self.binary = binary
        self.environment = VaniProcess.environment(configPath: configPath, stateDir: stateDir)
    }

    deinit { stop() }

    // MARK: one client per binary and paths

    private struct Identity: Hashable { let binary: URL, configPath: String?, stateDir: String? }
    private static let sharedLock = NSLock()
    nonisolated(unsafe) private static var current: (Identity, JobsServeClient)?

    /// The app's one serve for this binary, config and state folder. Asking for another set stops
    /// the old one: its marks and state are not the ones the tab now shows.
    public static func shared(binary: URL, configPath: String?, stateDir: String?) -> JobsServeClient {
        let id = Identity(binary: binary, configPath: configPath, stateDir: stateDir)
        sharedLock.lock()
        defer { sharedLock.unlock() }
        if let (have, client) = current, have == id { return client }
        current?.1.stop()
        let client = JobsServeClient(binary: binary, configPath: configPath, stateDir: stateDir)
        current = (id, client)
        return client
    }

    /// Why serve is no longer asked (it stopped twice, or speaks another protocol); nil while it is.
    public var unavailable: String? { lock.lock(); defer { lock.unlock() }; return disabled }
    /// Whether a request now goes to serve.
    public var isReady: Bool { lock.lock(); defer { lock.unlock() }; return ready }
    /// How many requests serve has answered (with a reply or the command's failure).
    public var answered: Int { lock.lock(); defer { lock.unlock() }; return answeredCount }
    /// The running serve's pid, for tests that end it.
    var pid: Int32? { lock.lock(); defer { lock.unlock() }; return running?.process.processIdentifier }

    /// Stops serve: its stdin is closed, so it exits once it has answered what it holds, and is
    /// terminated if it has not after the grace period. Waiting requests run the command.
    public func stop() {
        lock.lock()
        let r = running
        running = nil
        ready = false
        let waiting = pending
        pending = [:]
        lock.unlock()
        for w in waiting.values { w.done.signal() }
        guard let r else { return }
        r.closeInput()
        let p = r.process
        DispatchQueue.global().asyncAfter(deadline: .now() + VaniProcess.terminationGrace) {
            if p.isRunning { p.terminate() }
        }
    }

    // MARK: asking

    /// Asks serve for `vani jobs <op> --json <args>`. Nil means serve cannot answer now and the
    /// caller runs the command. Throws `VaniCallCancelled` when `cancellation` is set or serve
    /// replaced the request with a newer one, and the command's timeout when serve gave up on it.
    public func ask(op: String, args: [String], timeout: TimeInterval, cancellation: CancelFlag? = nil) throws -> Answer? {
        let name = "vani jobs \(op)"
        // As VaniProcess.run: a call cancelled before it began starts nothing.
        if cancellation?.isSet == true { throw VaniCallCancelled(command: name) }
        guard Self.ops.contains(op), let (id, waiter) = send(op: op, params: ["args": args]) else { return nil }
        let deadline = Date().addingTimeInterval(timeout)
        while waiter.done.wait(timeout: .now() + .milliseconds(25)) == .timedOut {
            if cancellation?.isSet == true {
                forget(id)
                throw VaniCallCancelled(command: name)
            }
            if Date() >= deadline {
                forget(id)
                throw BankError("\(name) did not finish within \(Int(timeout)) s")
            }
        }
        guard let line = waiter.line else { return nil } // serve died with the request open
        let answer = try Self.answer(line, id: id, name: name, timeout: timeout)
        if answer != nil {
            lock.lock()
            answeredCount += 1
            lock.unlock()
        }
        return answer
    }

    /// Reads one response line for request `id`. Internal so tests can feed it lines.
    static func answer(_ line: Data, id: String, name: String, timeout: TimeInterval) throws -> Answer? {
        if let result = okResult(line, id: id) { return .reply(result) }
        guard let r = try? JSONDecoder().decode(ServeResponse.self, from: line), r.id == id else { return nil }
        if r.ok {
            // Not the layout okResult reads: take the result through a parse. Its bytes may differ
            // from the command's, but its value does not.
            guard let object = (try? JSONSerialization.jsonObject(with: line)) as? [String: Any],
                  let result = object["result"],
                  let data = try? JSONSerialization.data(withJSONObject: result, options: [.fragmentsAllowed]) else { return nil }
            return .reply(data)
        }
        switch r.error?.code {
        case "E_COMMAND":
            return .failed(message: r.error?.message ?? "\(name) failed")
        case "E_SUPERSEDED":
            throw VaniCallCancelled(command: name)
        case "E_TIMEOUT":
            throw BankError("\(name) did not finish within \(Int(timeout)) s")
        default:
            return nil // busy, refused, internal: the command answers instead
        }
    }

    /// The result bytes of a success line as serve writes it, `{"id":"<id>","ok":true,"result":<R>}`,
    /// without parsing <R>: a list is megabytes, and the command's own bytes are what is decoded.
    static func okResult(_ line: Data, id: String) -> Data? {
        let head = Data(#"{"id":"\#(id)","ok":true,"result":"#.utf8)
        guard line.count > head.count + 1, line.starts(with: head), line.last == UInt8(ascii: "}") else { return nil }
        return line.subdata(in: (line.startIndex + head.count)..<(line.endIndex - 1))
    }

    /// The id a response line starts with (`{"id":"<id>",`), without parsing the line.
    static func lineID(_ line: Data) -> String? {
        let head = Data(#"{"id":""#.utf8)
        guard line.starts(with: head) else { return nil }
        let rest = line[(line.startIndex + head.count)...]
        guard let end = rest.firstIndex(of: UInt8(ascii: "\"")), end - rest.startIndex <= 32 else { return nil }
        return String(data: line.subdata(in: rest.startIndex..<end), encoding: .utf8)
    }

    static func requestLine(id: String, op: String, params: [String: Any]) -> Data? {
        guard var line = try? JSONSerialization.data(withJSONObject: ["id": id, "op": op, "params": params]) else { return nil }
        line.append(UInt8(ascii: "\n"))
        return line
    }

    struct ServeResponse: Decodable {
        let id: String
        let ok: Bool
        let error: ServeError?
    }

    struct ServeError: Decodable {
        let code: String
        let message: String
    }

    struct HelloResponse: Decodable {
        let id: String
        let ok: Bool
        let result: Hello?
    }

    struct Hello: Decodable {
        let `protocol`: Int
        let ops: [String]
    }

    /// Writes the request to a serve that has said hello and registers its waiter. Nil when
    /// serve is not to be asked now: disabled, backing off after a death, or not ready (in which
    /// case a start is begun in the background if none is under way).
    private func send(op: String, params: [String: Any]) -> (String, Waiter)? {
        lock.lock()
        guard disabled == nil else { lock.unlock(); return nil }
        guard let r = running else {
            startLocked()
            lock.unlock()
            return nil
        }
        guard ready, let (id, w, line) = registerLocked(op: op, params: params) else { lock.unlock(); return nil }
        lock.unlock()
        return writeRequest(line, id: id, to: r) ? (id, w) : nil
    }

    /// Registers a waiter for a new request and returns its line. Called with `lock` held.
    private func registerLocked(op: String, params: [String: Any]) -> (String, Waiter, Data)? {
        nextID += 1
        let id = String(nextID)
        guard let line = Self.requestLine(id: id, op: op, params: params) else { return nil }
        let w = Waiter()
        pending[id] = w
        return (id, w, line)
    }

    /// Writes a request line to serve, whole, without holding `lock`: serve reads stdin only while
    /// it can write stdout, and stdout is read by a thread that needs `lock`. A failed write means
    /// serve is gone; the request runs the command.
    private func writeRequest(_ line: Data, id: String, to r: Running) -> Bool {
        writeLock.lock()
        let ok = Self.writeAll(line, to: r.input)
        writeLock.unlock()
        if !ok {
            lock.lock()
            pending[id] = nil
            diedLocked(r.generation)
            lock.unlock()
        }
        return ok
    }

    private func forget(_ id: String) {
        lock.lock()
        pending[id] = nil
        lock.unlock()
    }

    // MARK: the process

    /// Spawns serve and asks for its hello on a thread of its own; requests run the command until
    /// it answers. Called with `lock` held and no process running.
    private func startLocked() {
        if let diedAt, Date().timeIntervalSince(diedAt) < Self.restartBackoff { return }
        guard starts < Self.maxStarts else {
            disabled = "vani jobs serve stopped twice; the Jobs tab runs each command instead"
            return
        }
        starts += 1
        let p = Process()
        p.executableURL = binary
        p.arguments = ["jobs", "serve"]
        p.environment = environment
        let stdin = Pipe(), stdout = Pipe()
        p.standardInput = stdin
        p.standardOutput = stdout
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch {
            diedAt = Date()
            return
        }
        // Foundation closes this process's copies of the child's ends (stdin's read end, stdout's
        // write end) once the child runs, so serve sees EOF when stdin is closed here and the
        // reader sees EOF when serve exits. They are not closed here a second time: a descriptor
        // number closed twice may by then belong to someone else.
        let r = Running(process: p, stdin: stdin.fileHandleForWriting, generation: starts)
        // A write after serve died must fail with EPIPE, not raise SIGPIPE in the app.
        _ = fcntl(r.input, F_SETNOSIGPIPE, 1)
        running = r
        ready = false
        let output = stdout.fileHandleForReading
        let reader = Thread { [weak self] in
            self?.read(output.fileDescriptor, generation: r.generation)
            try? output.close()
        }
        reader.stackSize = 256 * 1024
        reader.start()
        guard let (id, w, line) = registerLocked(op: "hello", params: ["protocol": Self.protocolVersion, "host": "VaniApply"]) else {
            diedLocked(r.generation)
            return
        }
        let greeter = Thread { [weak self] in
            guard let self, self.writeRequest(line, id: id, to: r) else { return }
            self.greet(r, id: id, waiter: w)
        }
        greeter.stackSize = 128 * 1024
        greeter.start()
    }

    /// Waits for serve's hello and marks it ready, or stops it: a serve that does not say hello,
    /// or speaks another protocol, is not asked anything.
    private func greet(_ r: Running, id: String, waiter: Waiter) {
        let answered = waiter.done.wait(timeout: .now() + Self.helloTimeout) == .success
        lock.lock()
        defer { lock.unlock() }
        pending[id] = nil
        guard running?.generation == r.generation else { return } // stopped or died meanwhile
        guard answered, let line = waiter.line,
              let hello = try? JSONDecoder().decode(HelloResponse.self, from: line), hello.id == id, hello.ok,
              let result = hello.result else {
            diedLocked(r.generation)
            return
        }
        guard result.protocol == Self.protocolVersion, Self.ops.isSubset(of: Set(result.ops)) else {
            disabled = "vani jobs serve speaks protocol \(result.protocol); this app speaks \(Self.protocolVersion). Rebuild the app."
            diedLocked(r.generation)
            return
        }
        ready = true
    }

    /// Reads response lines until serve's stdout ends, handing each to its waiter.
    private func read(_ fd: Int32, generation: Int) {
        var buffer = Data()
        var chunk = [UInt8](repeating: 0, count: 256 * 1024)
        while true {
            let n = Foundation.read(fd, &chunk, chunk.count)
            if n < 0, errno == EINTR { continue }
            if n <= 0 { break }
            buffer.append(contentsOf: chunk.prefix(n))
            while let nl = buffer.firstIndex(of: UInt8(ascii: "\n")) {
                let line = buffer.subdata(in: buffer.startIndex..<nl)
                buffer.removeSubrange(buffer.startIndex...nl)
                deliver(line)
            }
            if buffer.count > Self.maxLineBytes { break } // not a line serve writes: treat it as dead
        }
        lock.lock()
        diedLocked(generation)
        lock.unlock()
    }

    private func deliver(_ line: Data) {
        guard let id = Self.lineID(line) else { return }
        lock.lock()
        let w = pending.removeValue(forKey: id)
        lock.unlock()
        w?.line = line
        w?.done.signal()
    }

    /// Serve `generation` died or was given up: every waiting request runs the command, and a
    /// later one may start serve again after the backoff. Called with `lock` held.
    private func diedLocked(_ generation: Int) {
        guard let r = running, r.generation == generation else { return }
        running = nil
        ready = false
        diedAt = Date()
        let waiting = pending
        pending = [:]
        for w in waiting.values { w.done.signal() }
        r.closeInput()
        if r.process.isRunning { r.process.terminate() }
    }

    /// Writes all of `data` to the blocking `fd`; false on any error (EPIPE: serve is gone).
    static func writeAll(_ data: Data, to fd: Int32) -> Bool {
        var offset = 0
        while offset < data.count {
            let n = data.withUnsafeBytes { write(fd, $0.baseAddress! + offset, data.count - offset) }
            if n > 0 { offset += n; continue }
            if n < 0, errno == EINTR { continue }
            return false
        }
        return true
    }
}
