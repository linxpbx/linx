import Foundation
import Network

/// Whether the Wi-Fi the phone is on wants a sign-in page first (a hotel, an
/// airport, a plane, a ship): it answers everything itself until then, so
/// Linx can't be reached and the phone would only say it is trying again.
///
/// Asked only when the line couldn't be had, and only over HTTPS: Apple's
/// own "success" page is fetched, and a Wi-Fi that hasn't been signed in to
/// either breaks the TLS (it can't show Apple's certificate) or answers with
/// a page of its own. Nothing is ever sent in the clear.
enum CaptivePortal {
    static let probe = URL(string: "https://www.apple.com/library/test/success.html")!

    /// Whether the phone is on Wi-Fi that hasn't been signed in to.
    static func suspected() async -> Bool {
        guard await onWiFi() else { return false }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 6
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        do {
            let (data, response) = try await session.data(from: probe)
            guard let http = response as? HTTPURLResponse else { return false }
            return http.url?.host != probe.host || !String(decoding: data, as: UTF8.self).contains("Success")
        } catch let error as URLError {
            return answersForEveryone(error)
        } catch {
            return false
        }
    }

    /// The errors of a network that answers in Apple's place, as opposed to
    /// one that isn't there at all.
    static func answersForEveryone(_ error: URLError) -> Bool {
        switch error.code {
        case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateHasUnknownRoot,
            .serverCertificateNotYetValid, .secureConnectionFailed, .appTransportSecurityRequiresSecureConnection:
            return true
        default:
            return false
        }
    }

    private static func onWiFi() async -> Bool {
        await withCheckedContinuation { done in
            let monitor = NWPathMonitor()
            monitor.pathUpdateHandler = { path in
                monitor.cancel()
                done.resume(returning: path.status == .satisfied && path.usesInterfaceType(.wifi))
            }
            monitor.start(queue: DispatchQueue(label: "linx.captive-portal"))
        }
    }

    static let words =
        "This Wi-Fi needs you to sign in first. Open Safari, sign in to the Wi-Fi, and Linx will connect."
}
