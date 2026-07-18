import Foundation

/// Talks to a running `cmd/facet` host process over its already-shipped
/// browser-facing HTTP surface (`POST /api/dev-login`, `GET /api/feed`) —
/// no NATS/crypto client of its own. That surface is exactly what makes a
/// second renderer possible without reimplementing EDGE.3's transport: the
/// Go host owns auth + the wire connection; any renderer that can speak
/// HTTP + SSE can hydrate from it, PWA or native.
public final class FeedClient {
    private let baseURL: URL
    private let session: URLSession

    /// The `facet_session` cookie (`name=value`) captured off `devLogin`'s
    /// `Set-Cookie` response header and replayed explicitly on every later
    /// request. Not delegated to `URLSession`'s automatic cookie jar: an
    /// `.ephemeral` session's jar did not reliably replay a cookie set by
    /// one `data(for:)` call onto a later request in this toolchain
    /// (observed live against a running `cmd/facet` host — `dev-login`
    /// returned 200 but `/api/feed` still 401'd) — explicit capture-and-
    /// replay sidesteps whatever that jar's actual scoping rule is instead
    /// of guessing at it.
    private var sessionCookie: String?

    public init(baseURL: URL) {
        self.baseURL = baseURL
        self.session = URLSession(configuration: .ephemeral)
    }

    /// `POST /api/dev-login {identityId}` — the demo-only login stand-in
    /// (`cmd/facet/session.go`'s `handleDevLogin`, gated on
    /// `FACET_DEV_AUTH`). Captures the `facet_session` cookie from the
    /// response for `stream()` to replay.
    public func devLogin(identityID: String) async throws {
        var req = URLRequest(url: baseURL.appendingPathComponent("api/dev-login"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["identityId": identityID])
        let (_, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            throw FeedClientError.loginFailed
        }
        guard let setCookie = http.value(forHTTPHeaderField: "Set-Cookie"),
              let cookiePair = setCookie.split(separator: ";").first else {
            throw FeedClientError.loginFailed
        }
        sessionCookie = String(cookiePair)
    }

    /// `GET /api/feed` — streams the manifest snapshot then live deltas
    /// until cancelled, mirroring `edge-source.mjs`'s `start()`: connect
    /// once, replay burst, then live frames, order-independent because
    /// every reducer downstream is last-write-wins per key.
    ///
    /// Built on a delegate-based `URLSessionDataTask` rather than
    /// `URLSession.bytes(for:)`: the async-sequence API was observed, live
    /// against a running `cmd/facet` host, to never yield a single line for
    /// this long-lived, connection-never-closes SSE response (curl streamed
    /// it instantly) — a delegate's `didReceive data:` callbacks fire
    /// incrementally as bytes actually arrive, which is what SSE needs.
    public func stream() -> AsyncThrowingStream<ManifestFrame, Error> {
        AsyncThrowingStream { continuation in
            guard let cookie = sessionCookie else {
                continuation.finish(throwing: FeedClientError.notLoggedIn)
                return
            }
            var req = URLRequest(url: baseURL.appendingPathComponent("api/feed"))
            req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
            req.setValue(cookie, forHTTPHeaderField: "Cookie")

            let delegate = SSEStreamDelegate(continuation: continuation)
            let streamSession = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
            let task = streamSession.dataTask(with: req)
            continuation.onTermination = { [delegate, streamSession] _ in
                _ = delegate // retained for the stream's lifetime
                streamSession.invalidateAndCancel()
            }
            task.resume()
        }
    }
}

/// Incrementally decodes one `GET /api/feed` response body into
/// `ManifestFrame`s as bytes arrive, bridging the delegate callback world
/// into the `AsyncThrowingStream` `stream()` exposes.
private final class SSEStreamDelegate: NSObject, URLSessionDataDelegate {
    private let continuation: AsyncThrowingStream<ManifestFrame, Error>.Continuation
    private let decoder = SSEDecoder()
    private var buffer = Data()

    init(continuation: AsyncThrowingStream<ManifestFrame, Error>.Continuation) {
        self.continuation = continuation
    }

    func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            continuation.finish(throwing: FeedClientError.streamFailedStatus(http.statusCode))
            completionHandler(.cancel)
            return
        }
        completionHandler(.allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        buffer.append(data)
        while let newline = buffer.firstIndex(of: 0x0A) {
            let lineData = buffer.subdata(in: buffer.startIndex..<newline)
            buffer.removeSubrange(buffer.startIndex...newline)
            var line = String(data: lineData, encoding: .utf8) ?? ""
            if line.hasSuffix("\r") { line.removeLast() }
            if let frame = decoder.feed(line: line) {
                continuation.yield(frame)
            }
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if let error, (error as NSError).code != NSURLErrorCancelled {
            continuation.finish(throwing: error)
        } else {
            continuation.finish()
        }
    }
}

public enum FeedClientError: Error {
    case loginFailed
    case notLoggedIn
    case streamFailedStatus(Int)
}
