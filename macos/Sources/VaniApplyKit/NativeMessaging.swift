import Foundation

// Chrome native messaging framing: each message is a 32-bit length in native
// byte order (little-endian on Apple silicon and Intel) followed by that many
// bytes of UTF-8 JSON. Chrome refuses a host-to-extension message over 1 MB.

public enum NativeMessaging {
    /// Chrome's limit for one message from the host.
    public static let maxReplyBytes = 1024 * 1024
    /// Our limit for one message from the extension.
    public static let maxRequestBytes = 64 * 1024 * 1024
    /// When reply body exceeds this, chunking is used to stay safely under Chrome's 1 MB limit.
    public static let chunkThreshold = 750_000
    public static let chunkSize = 500_000

    /// Room kept for the reply envelope (rev, indexes, totals) around a chunk's JSON string.
    static let chunkEnvelopeHeadroom = 4096

    /// Bytes a UTF-8 byte costs once its chunk is written as a JSON string: quote, backslash and
    /// slash become two, control characters at most six; everything else is itself.
    private static func escapedCost(_ b: UInt8) -> Int {
        switch b {
        case 0x22, 0x5C, 0x2F: return 2
        case 0..<0x20: return 6
        default: return 1
        }
    }

    /// The one owner of chunk boundaries: `get` reports `chunkRanges(of:).count` and `getChunk(i)`
    /// slices `chunkRanges(of:)[i]`, so the two cannot disagree. A chunk never splits a UTF-8
    /// scalar (each decodes to exactly its bytes), holds at most `chunkSize` raw bytes, and its
    /// JSON-string form fits `maxReplyBytes` with `chunkEnvelopeHeadroom` to spare, even when every
    /// byte is one that escapes. Always at least one range; the ranges tile `0..<data.count`.
    public static func chunkRanges(of data: Data) -> [Range<Int>] {
        let budget = maxReplyBytes - chunkEnvelopeHeadroom
        var ranges: [Range<Int>] = []
        data.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            var start = 0, cost = 0, i = 0
            while i < raw.count {
                // One scalar: a lead byte plus its continuation bytes (0b10xxxxxx).
                var end = i + 1
                while end < raw.count, raw[end] & 0xC0 == 0x80 { end += 1 }
                let scalarCost = end - i == 1 ? escapedCost(raw[i]) : end - i
                if i > start, i - start + (end - i) > chunkSize || cost + scalarCost > budget {
                    ranges.append(start..<i)
                    start = i
                    cost = 0
                }
                cost += scalarCost
                i = end
            }
            ranges.append(start..<raw.count)
        }
        return ranges
    }

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
