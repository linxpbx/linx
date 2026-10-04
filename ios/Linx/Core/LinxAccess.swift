import Foundation

/// How a screen reaches Linx: this phone's own server, and a token that is
/// always fresh (`PhoneSession` renews it, and the phone's certificate with
/// it, without anybody being asked anything).
///
/// Everything the screens read goes through one of these, so there is one
/// place where "which server, as whom" is decided — and a screenshot run or
/// a test hands over a stand-in instead of reaching a network at all.
@MainActor struct LinxAccess: Sendable {
    let client: LinxClient
    let server: URL
    let token: @Sendable () async throws -> String
}
