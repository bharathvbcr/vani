import Foundation

// Chrome native messaging framing: each message is a 32-bit length in native
// byte order (little-endian on Apple silicon and Intel) followed by that many
// bytes of UTF-8 JSON. Chrome refuses a host-to-extension message over 1 MB.

public enum NativeMessaging {
    /// Chrome's limit for one message from the host.
    public static let maxReplyBytes = 1024 * 1024
    /// Our limit for one message from the extension: a 900 KB bank plus its envelope.
    public static let maxRequestBytes = 2 * 1024 * 1024

    public enum ReadError: Error, Equatable {
        case truncated
        case tooLarge(Int)
    }

    /// Read one framed message; nil at a clean end of input.
    public static func readMessage(from h: FileHandle) throws -> Data? {
        guard let head = try readExactly(h, 4, allowEOF: true) else { return nil }
        let n = Int(head.withUnsafeBytes { $0.loadUnaligned(as: UInt32.self) }.littleEndian)
        if n > maxRequestBytes {
            // Drain it so the stream stays in step, then refuse.
            var left = n
            while left > 0 {
                guard let chunk = try h.read(upToCount: min(left, 64 * 1024)), !chunk.isEmpty else { throw ReadError.truncated }
                left -= chunk.count
            }
            throw ReadError.tooLarge(n)
        }
        guard let body = try readExactly(h, n, allowEOF: false) else { throw ReadError.truncated }
        return body
    }

    public static func frame(_ payload: Data) -> Data {
        var len = UInt32(payload.count).littleEndian
        var out = Data(bytes: &len, count: 4)
        out.append(payload)
        return out
    }

    /// Encode a reply, replacing one that would exceed Chrome's limit with an error.
    public static func encodeReply(_ reply: [String: Any]) -> Data {
        let body: Data
        if let d = try? JSONSerialization.data(withJSONObject: reply, options: [.withoutEscapingSlashes]),
           d.count <= maxReplyBytes {
            body = d
        } else {
            let fallback: [String: Any] = ["error": "reply too large for Chrome (over 1 MB); delete some answers"]
            body = (try? JSONSerialization.data(withJSONObject: fallback)) ?? Data(#"{"error":"reply encoding failed"}"#.utf8)
        }
        return frame(body)
    }

    private static func readExactly(_ h: FileHandle, _ n: Int, allowEOF: Bool) throws -> Data? {
        var out = Data()
        out.reserveCapacity(n)
        while out.count < n {
            guard let chunk = try h.read(upToCount: n - out.count), !chunk.isEmpty else {
                if out.isEmpty && allowEOF { return nil }
                throw ReadError.truncated
            }
            out.append(chunk)
        }
        return out
    }
}
