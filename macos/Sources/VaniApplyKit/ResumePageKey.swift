import Foundation

// The per-job resume key. `pageKey` is the Swift twin of extension/src/applied.ts
// pageKey (and of internal/joburl.Normalize): scheme, host, path and the job-naming
// query parameters. `postingKey` is the twin of applied.ts postingKey, which is the
// key a resume is stored under. On Workday one posting has several routes, so they
// fold to one key; everywhere else postingKey is pageKey unchanged.
//
// Foundation's URL normalises paths and default ports differently from the WHATWG
// parser the extension uses, so this is a small parser of its own. The shared cases
// are extension/test/vectors/joburl.json.

public enum ResumePageKey {
    public static let maxBytes = 2048

    private static let idParams = ["gh_jid", "jid", "job_id", "jobid", "req_id"]
    private static let idValue = #"^[A-Za-z0-9_-]{1,64}$"#
    private static let boardValue = #"^[A-Za-z0-9_-]{1,100}$"#
    private static let tokenValue = #"^[0-9]{1,20}$"#
    private static let localeValue = #"^[a-z]{2}-[A-Z]{2}$"#
    private static let sharedHost = #"^wd[0-9]{1,3}\.myworkdaysite\.com$"#

    /// Scheme, host, path and the job-naming parameters, or nil. A Greenhouse embedded
    /// form is the posting it embeds.
    public static func pageKey(_ raw: String) -> String? {
        guard raw.utf16.count <= maxBytes, raw.utf8.count <= maxBytes else { return nil }
        let s = trim(raw)
        guard !s.isEmpty, !refused(s), hasHTTPHost(s), let parts = split(s) else { return nil }
        var host = parts.host
        if host == "job-boards.greenhouse.io" { host = "boards.greenhouse.io" }
        let path = normalizePath(parts.path)
        let hostPort = parts.port.map { "\(host):\($0)" } ?? host
        if hostPort == "boards.greenhouse.io", path == "/embed/job_app" {
            let q = formMap(parts.query)
            let board = q["for"] ?? ""
            let token = q["token"] ?? ""
            if matches(board, boardValue), matches(token, tokenValue) {
                return "\(parts.scheme)://\(hostPort)/\(board)/jobs/\(token)"
            }
        }
        let q = formMap(parts.query)
        var kept: [String] = []
        for name in idParams {
            if let v = q[name], matches(v, idValue) { kept.append("\(name)=\(v)") }
        }
        let query = kept.isEmpty ? "" : "?" + kept.joined(separator: "&")
        return "\(parts.scheme)://\(hostPort)\(path)\(query)"
    }

    /// One key per posting. The pageKey string itself when the page is not a Workday
    /// posting, so a caller can compare it without a second serialisation.
    public static func postingKey(_ raw: String) -> String? {
        guard let page = pageKey(raw) else { return nil }
        guard let parts = split(page) else { return page }
        let shared = matches(parts.host, sharedHost)
        guard shared || parts.host.hasSuffix(".myworkdayjobs.com") else { return page }
        var segs = normalizePath(parts.path).split(separator: "/", omittingEmptySubsequences: true).map(String.init)
        if let first = segs.first, matches(first, localeValue) { segs.removeFirst() }
        let site: String
        if shared {
            guard segs.count >= 3, segs[0] == "recruiting" else { return page }
            site = segs[0...2].joined(separator: "/")
            segs = Array(segs.dropFirst(3))
        } else {
            guard let first = segs.first else { return page }
            site = first
            segs.removeFirst()
        }
        guard let kind = segs.first, kind == "job" || kind == "details" else { return page }
        let rest = Array(segs.dropFirst())
        let cut = rest.firstIndex { $0.lowercased() == "apply" } ?? rest.count
        let posting = Array(rest.prefix(cut))
        guard let slug = posting.last, !slug.isEmpty else { return page }
        let hostPort = parts.port.map { "\(parts.host):\($0)" } ?? parts.host
        return "\(parts.scheme)://\(hostPort)/\(site)/job/\(slug)"
    }

    // MARK: - Parsing

    private struct Parts {
        var scheme: String
        var host: String
        var port: Int?
        var path: String
        var query: String
    }

    /// JS `String.prototype.trim`: WhiteSpace, line terminators and U+FEFF.
    private static func trim(_ s: String) -> String {
        var set = CharacterSet.whitespacesAndNewlines
        set.insert(charactersIn: "\u{FEFF}")
        return s.trimmingCharacters(in: set)
    }

    /// The characters applied.ts refuses before it parses: whitespace, controls, `<`, `>` and `\`.
    private static func refused(_ s: String) -> Bool {
        s.unicodeScalars.contains { scalar in
            if scalar == "<" || scalar == ">" || scalar == "\\" { return true }
            if scalar.value <= 0x1f || (scalar.value >= 0x7f && scalar.value <= 0x9f) { return true }
            return CharacterSet.whitespacesAndNewlines.contains(scalar) || scalar == "\u{00A0}" || scalar == "\u{FEFF}"
        }
    }

    /// `https?://` plus one character that can start a host.
    private static func hasHTTPHost(_ s: String) -> Bool {
        guard let range = s.range(of: #"^https?://"#, options: [.regularExpression, .caseInsensitive]) else { return false }
        guard let c = s[range.upperBound...].first else { return false }
        return c != "/" && c != "?" && c != "#" && !c.isWhitespace
    }

    private static func split(_ s: String) -> Parts? {
        guard let marker = s.range(of: "://") else { return nil }
        let scheme = s[..<marker.lowerBound].lowercased()
        guard scheme == "http" || scheme == "https" else { return nil }
        var rest = s[marker.upperBound...]
        if let hash = rest.firstIndex(of: "#") { rest = rest[..<hash] }
        let query: String
        if let q = rest.firstIndex(of: "?") {
            query = String(rest[rest.index(after: q)...])
            rest = rest[..<q]
        } else {
            query = ""
        }
        let authority: String
        let path: String
        if let slash = rest.firstIndex(of: "/") {
            authority = String(rest[..<slash])
            path = String(rest[slash...])
        } else {
            authority = String(rest)
            path = ""
        }
        var auth = authority
        if let at = auth.lastIndex(of: "@") { auth = String(auth[auth.index(after: at)...]) }
        let hostRaw: String
        let port: Int?
        if auth.hasPrefix("[") {
            guard let end = auth.firstIndex(of: "]") else { return nil }
            hostRaw = String(auth[...end])
            let after = auth[auth.index(after: end)...]
            if after.isEmpty {
                port = nil
            } else if after.hasPrefix(":"), let n = portNumber(String(after.dropFirst())) {
                port = n
            } else {
                return nil
            }
        } else if let colon = auth.lastIndex(of: ":") {
            hostRaw = String(auth[..<colon])
            guard let n = portNumber(String(auth[auth.index(after: colon)...])) else { return nil }
            port = n
        } else {
            hostRaw = auth
            port = nil
        }
        var host = hostRaw.lowercased()
        while host.hasSuffix(".") { host.removeLast() }
        guard !host.isEmpty else { return nil }
        let standard = scheme == "https" ? 443 : 80
        return Parts(scheme: scheme, host: host, port: port == standard ? nil : port, path: path, query: query)
    }

    private static func portNumber(_ s: String) -> Int? {
        guard !s.isEmpty, s.allSatisfy(\.isNumber), let n = Int(s), n <= 65535 else { return nil }
        return n
    }

    /// WHATWG path serialisation, then the extension's own slash collapse and trailing-slash strip.
    private static func normalizePath(_ raw: String) -> String {
        guard !raw.isEmpty else { return "/" }
        let parts = raw.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
        var out: [String] = []
        for (i, part) in parts.enumerated() {
            if i == 0, part.isEmpty { continue }
            let decoded = percentDecode(part)
            if decoded == "." { continue }
            if decoded == ".." {
                if !out.isEmpty { out.removeLast() }
                continue
            }
            out.append(pathEncode(decoded))
        }
        var path = "/" + out.joined(separator: "/")
        while path.contains("//") { path = path.replacingOccurrences(of: "//", with: "/") }
        if path.count > 1, path.hasSuffix("/") { path.removeLast() }
        return path.isEmpty ? "/" : path
    }

    /// `application/x-www-form-urlencoded`, first value wins, the way `URLSearchParams.get` does.
    private static func formMap(_ query: String) -> [String: String] {
        var out: [String: String] = [:]
        guard !query.isEmpty else { return out }
        for pair in query.split(separator: "&", omittingEmptySubsequences: false) {
            let text = String(pair)
            guard !text.isEmpty else { continue }
            let name: String
            let value: String
            if let eq = text.firstIndex(of: "=") {
                name = formDecode(String(text[..<eq]))
                value = formDecode(String(text[text.index(after: eq)...]))
            } else {
                name = formDecode(text)
                value = ""
            }
            if out[name] == nil { out[name] = value }
        }
        return out
    }

    private static func formDecode(_ s: String) -> String {
        percentDecode(s.replacingOccurrences(of: "+", with: " "))
    }

    private static func percentDecode(_ s: String) -> String {
        var bytes: [UInt8] = []
        let chars = Array(s)
        var i = 0
        while i < chars.count {
            if chars[i] == "%", i + 2 < chars.count,
               let hi = chars[i + 1].hexDigitValue, let lo = chars[i + 2].hexDigitValue {
                bytes.append(UInt8((hi << 4) | lo))
                i += 3
                continue
            }
            bytes.append(contentsOf: String(chars[i]).utf8)
            i += 1
        }
        return String(decoding: bytes, as: UTF8.self)
    }

    /// Bytes the URL path serializer leaves as themselves. Everything else is `%HH`.
    private static func pathEncode(_ s: String) -> String {
        var out = ""
        for byte in s.utf8 {
            if pathByte(byte) {
                out.append(Character(UnicodeScalar(byte)))
            } else {
                out.append(String(format: "%%%02X", byte))
            }
        }
        return out
    }

    private static func pathByte(_ b: UInt8) -> Bool {
        if b <= 0x1f || b >= 0x7f { return false }
        switch b {
        case 0x20, 0x22, 0x23, 0x3c, 0x3e, 0x3f, 0x60, 0x7b, 0x7d: return false
        default: return true
        }
    }

    private static func matches(_ s: String, _ pattern: String) -> Bool {
        s.range(of: pattern, options: .regularExpression) != nil
    }
}
