import Foundation
import SwiftUI

// What the app is doing, for the screens to follow (docs/PHASE2.md §4). The
// whole app has one of these: it holds the phone's identity, asks Linx for
// the token and the phone line, and says in one word which screen belongs on
// the display.

@MainActor @Observable final class AppModel {
    enum State: Equatable {
        /// No phone set up yet: the setup screen.
        case setUp
        /// Busy with a setup code, or signing in again after a restart.
        case working(String)
        /// Set up and in touch with Linx.
        case signedIn
        /// Linx says this phone has to be set up again (six months with no
        /// contact, a changed password, or an admin stopped it).
        case setUpAgain(String)
    }

    /// The app's one model. A push can arrive before any screen exists, so
    /// the delegate and the views have to be looking at the same one
    /// (`AppDelegate`).
    static let shared = AppModel()

    var state: State = .setUp
    /// Who this phone is, once it has been set up.
    var identity: PhoneIdentity?
    /// This phone's SIP login, in memory only and new every time.
    var line: PhoneLine?
    /// What to tell the person about the last thing that didn't work.
    var problem: String?

    /// The phone line itself: signing in to Asterisk, and the calls
    /// (`Features/Phone`). It asks this model for a line whenever it needs
    /// one, which is also what notices a phone that has been stopped.
    @ObservationIgnored lazy var phone: PhoneModel = PhoneModel(line: { [weak self] in await self?.lineForPhone() })

    /// What the four tabs share: the team, this person's status and the two
    /// counts on the tab bar (docs/PHASE2.md §12 step 7). It is rebuilt
    /// whenever this phone's identity changes, because everything it reads
    /// is read as that person.
    var home = HomeModel(access: nil)
    /// Which phone the model above belongs to, so signing in again — which
    /// happens whenever a push wakes a phone that wasn't signed in — doesn't
    /// quietly replace a live one that a screen is already watching.
    private var homeFor: UUID?

    private let session: PhoneSession
    private let push: PushService
    /// The last tokens Linx was told about, so the app doesn't say the same
    /// thing again every time it signs in.
    private var toldLinx: PushTokens?

    init(session: PhoneSession = PhoneSession(), push: PushService = .shared) {
        self.session = session
        self.push = push
    }

    // MARK: - Calls to a sleeping phone (docs/PHASE2.md §5, build step 6)

    /// listenForCalls runs once, at the very first moment of the app's life
    /// (`AppDelegate`): PushKit only delivers a call to an app that was
    /// already listening for one.
    func listenForCalls() {
        push.onCall = { [weak self] _, from, done in
            Task { @MainActor in
                await self?.woken(from: from)
                // Apple's "I'm finished with this push", once the phone is
                // ringing. Opening the line goes on in the background.
                done()
            }
        }
        push.onTokens = { [weak self] tokens in
            Task { @MainActor in await self?.tellLinxWhereToReachThisPhone(tokens) }
        }
        push.start()
    }

    /// woken is a call arriving at a phone whose app was asleep or closed.
    /// The phone rings first — that is all that happens here — and the line
    /// is opened afterwards, because the call is held ringing for a few
    /// seconds at the server's end (docs/PHASE2.md §5).
    func woken(from: String) async {
        guard await phone.woken(from: from) else { return }
        Task { [weak self] in await self?.openTheLineForACall() }
    }

    private func openTheLineForACall() async {
        if identity == nil {
            // Launched from nothing by the push: this is the whole of
            // signing in, and it is the same path as an ordinary start.
            await start()
        } else if case .signedIn = state {
            await phone.start()
        } else {
            await signIn()
        }
    }

    /// askAboutNotifications asks iOS, once, for the quiet notifications: a
    /// missed call and a new voicemail. It does nothing at all unless this
    /// phone is signed in and the app is in front, and nothing a second
    /// time, so it is safe to call whenever either of those becomes true.
    func askAboutNotifications() async {
        guard case .signedIn = state else { return }
        await push.askAboutNotifications()
        if let tokens = push.current { await tellLinxWhereToReachThisPhone(tokens) }
    }

    /// tellLinxWhereToReachThisPhone sends Apple's tokens on to Linx
    /// (`POST /api/v1/me/phone-push`). A phone that isn't signed in yet
    /// keeps them until it is.
    func tellLinxWhereToReachThisPhone(_ tokens: PushTokens) async {
        guard let identity, tokens != toldLinx else { return }
        do {
            let token = try await session.accessToken()
            try await LinxClient(server: identity.server).setPhonePush(tokens, token: token)
            toldLinx = tokens
        } catch {
            // Nothing to tell the person: the app says it again next time
            // it signs in, which is every time it starts.
        }
    }

    /// linx is how the app's screens ask Linx for things — the team, call
    /// history, voicemail (docs/PHASE2.md §12 step 7). The token comes from
    /// the session, which keeps it fresh and renews this phone's certificate
    /// on its own, so no screen ever has to think about signing in.
    var linx: LinxAccess? {
        guard let identity else { return nil }
        let session = self.session
        return LinxAccess(
            client: LinxClient(server: identity.server), server: identity.server,
            token: { try await session.accessToken() })
    }

    /// start runs once, as the app opens: if this phone is set up, it signs
    /// itself in and asks for its phone line. Nothing is typed, ever.
    func start() async {
        await session.restore()
        guard let identity = await session.identity else {
            state = .setUp
            return
        }
        self.identity = identity
        state = .working("Signing in…")
        await signIn()
    }

    /// setUp redeems a setup code: one key, one certificate, and the phone is
    /// this person's extension from then on.
    func setUp(with code: SetupCode) async {
        problem = nil
        state = .working("Setting up this phone…")
        do {
            identity = try await session.setUp(with: code)
            await signIn()
        } catch let error as LinxError {
            problem = error.words
            state = .setUp
        } catch DeviceKeyError.noSecureEnclave {
            problem = "This device has no Secure Enclave, so Linx can't keep a key safely on it."
            state = .setUp
        } catch {
            problem = "Setting this phone up didn't finish. Ask for a new setup code and try again."
            state = .setUp
        }
    }

    /// signIn proves this phone is still itself, and then opens its phone
    /// line. A phone that is no longer set up is told so here, which is the
    /// only way it ever finds out.
    func signIn() async {
        do {
            _ = try await session.accessToken()
            problem = nil
            state = .signedIn
            if homeFor != identity?.deviceID {
                home.stop()
                home = HomeModel(access: linx)
                homeFor = identity?.deviceID
            }
            await phone.start()
            // Now that there is a phone here: ask about notifications once,
            // and tell Linx where Apple can reach it. Signing in happens as
            // the app launches, when it isn't in front yet and iOS won't
            // show its prompt, so the question is asked again the moment the
            // app really is in front (`LinxApp`'s scene phase).
            await askAboutNotifications()
            if let tokens = push.current { await tellLinxWhereToReachThisPhone(tokens) }
        } catch let error as LinxError where error.setUpAgain {
            state = .setUpAgain(error.words)
        } catch let error as LinxError {
            problem = error.words
            // Still signed in as far as the phone knows: it keeps its
            // certificate and tries again (a hotel Wi-Fi, a lost signal).
            state = identity == nil ? .setUp : .signedIn
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
            state = identity == nil ? .setUp : .signedIn
        }
    }

    /// lineForPhone asks Linx for this phone's SIP login — a new password
    /// every time, kept in memory only (docs/PHASE2.md §4). The phone asks
    /// again whenever it has to open its line afresh, so a phone that has
    /// been stopped finds out within seconds.
    func lineForPhone() async -> PhoneModel.Line? {
        guard let identity else { return nil }
        do {
            let line = try await session.phoneLine()
            let token = try await session.accessToken()
            self.line = line
            problem = nil
            return PhoneModel.Line(line: line, server: identity.server, token: token)
        } catch let error as LinxError where error.setUpAgain {
            state = .setUpAgain(error.words)
            return nil
        } catch let error as LinxError {
            problem = error.words
            return nil
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
            return nil
        }
    }

    /// signOut forgets this phone's key and certificate. Stopping the phone
    /// at Linx's end is My phones → "I've lost it" in the web app, which is
    /// what to use for a phone someone else has.
    func signOut() async {
        phone.stop()
        home.stop()
        home = HomeModel(access: nil)
        homeFor = nil
        await session.forget()
        identity = nil
        line = nil
        problem = nil
        toldLinx = nil
        state = .setUp
    }
}
