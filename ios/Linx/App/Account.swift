import Foundation
import SwiftUI

// One Linx account on this phone (docs/MULTI_ACCOUNT.md §3). A phone can be
// set up on several Linx servers at once — a live system and a staging one,
// two companies — and each of them is a separate enrolment with its own key,
// its own certificate, its own phone line and its own team. Everything that
// belongs to one of them is here; `AppModel` holds the list of them and
// nothing else.
//
// All of this was `AppModel` until 2026-10-19, when it was lifted out
// unchanged: one account behaves exactly as the whole app did before.

@MainActor @Observable final class Account: Identifiable {
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

    /// What the screens tell this account by. It is the app's own, not
    /// Linx's, because an account being set up has no id of Linx's yet.
    let id = UUID()
    /// The account's id at Linx — the device id it issued, which is what
    /// the Keychain, the push payload and the accounts list all name it by.
    /// Known from the start for an account already on this phone, and nil
    /// for one being set up, until Linx has issued it.
    var accountID: UUID? { identity?.deviceID ?? storedID }
    /// The id this account was restored under, before anything has been
    /// read out of the Keychain.
    private let storedID: UUID?

    var state: State = .setUp
    /// Who this phone is, once it has been set up.
    var identity: PhoneIdentity?
    /// This phone's SIP login, in memory only and new every time.
    var line: PhoneLine?
    /// What to tell the person about the last thing that didn't work.
    var problem: String?

    /// The phone line itself: signing in to Asterisk, and the calls
    /// (`Features/Phone`). It asks this account for a line whenever it needs
    /// one, which is also what notices a phone that has been stopped.
    @ObservationIgnored lazy var phone: PhoneModel = PhoneModel(line: { [weak self] in await self?.lineForPhone() })

    /// What the four tabs show for this account: the team, this person's
    /// status and the two counts on the tab bar (docs/PHASE2.md §12 step 7).
    /// It is rebuilt whenever this phone's identity changes, because
    /// everything it reads is read as that person.
    var home = HomeModel(access: nil)
    /// Which phone the model above belongs to, so signing in again — which
    /// happens whenever a push wakes a phone that wasn't signed in — doesn't
    /// quietly replace a live one that a screen is already watching.
    private var homeFor: UUID?

    private let session: PhoneSession
    /// Called once this account has signed in. Asking iOS about
    /// notifications, and telling every account's server where Apple can
    /// reach this phone, is the app's business and not one account's: iOS
    /// asks once for the whole app and gives it one token (`AppModel`).
    @ObservationIgnored var onSignedIn: (() async -> Void)?
    /// The last tokens Linx was told about, so the app doesn't say the same
    /// thing again every time it signs in. Each account's server is told
    /// separately: Apple gives the app one token and both servers need it.
    private var toldLinx: PushTokens?

    /// `storedAs` names one of the phone's existing enrolments
    /// (`PhoneStore.accounts`); an account being set up has none yet.
    init(session: PhoneSession = PhoneSession(), storedAs storedID: UUID? = nil) {
        self.session = session
        self.storedID = storedID
    }

    // MARK: - Calls to a sleeping phone (docs/PHASE2.md §5, build step 6)

    /// woken is a call arriving at a phone whose app was asleep or closed.
    /// The phone rings first — that is all that happens here — and the line
    /// is opened afterwards, because the call is held ringing for a few
    /// seconds at the server's end (docs/PHASE2.md §5).
    func woken(from: String, video: Bool = false) async {
        guard await phone.woken(from: from, video: video) else { return }
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

    /// Crash reports iOS handed over go to Linx once this phone is signed
    /// in (`CrashReports`), with the same token as everything else.
    private func sendCrashReportsFromNowOn() {
        guard let identity else { return }
        let session = self.session
        if let note = SignInTrail.pending { CrashReports.shared.add(note) }
        CrashReports.shared.send = { report in
            do {
                let token = try await session.accessToken()
                try await LinxClient(server: identity.server).sendCallReport(report, token: token)
                return true
            } catch {
                return false
            }
        }
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
        if let missing = await session.restore() {
            // Locked storage is a phone woken before its first unlock: wait
            // for the unlock instead of asking to be set up again.
            if missing.locked {
                state = .working("Waiting for this iPhone to be unlocked…")
                for await _ in NotificationCenter.default.notifications(
                    named: UIApplication.protectedDataDidBecomeAvailableNotification)
                {
                    break
                }
                await start()
                return
            }
            if SignInTrail.missing(identity: missing.identity, key: missing.key, why: missing.why) != nil {
                // Read the storage once more before giving up on it.
                try? await Task.sleep(for: .seconds(2))
                if await session.restore() == nil, let identity = await session.identity {
                    SignInTrail.setUp()
                    self.identity = identity
                    state = .working("Signing in…")
                    await signIn()
                    return
                }
            }
            state = .setUp
            await setUpForTheBench()
            return
        }
        guard let identity = await session.identity else {
            state = .setUp
            await setUpForTheBench()
            return
        }
        SignInTrail.setUp()
        self.identity = identity
        state = .working("Signing in…")
        await signIn()
    }

    /// The test bench sets itself up from the link it was started with
    /// (`Bench`); nothing happens in an ordinary run.
    private func setUpForTheBench() async {
        guard let link = Bench.setupLink, let code = SetupCode.link(link) else { return }
        await setUp(with: code)
    }

    /// setUp redeems a setup code: one key, one certificate, and the phone is
    /// this person's extension from then on.
    func setUp(with code: SetupCode) async {
        problem = nil
        state = .working("Setting up this phone…")
        do {
            identity = try await session.setUp(with: code)
            SignInTrail.setUp()
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
            sendCrashReportsFromNowOn()
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
            await onSignedIn?()
        } catch let error as LinxError where error.setUpAgain {
            stopped(error.words)
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
            stopped(error.words)
            return nil
        } catch let error as LinxError {
            problem = error.words
            return nil
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
            return nil
        }
    }

    /// stopped is Linx saying this phone may no longer be used: the line
    /// closes and a call on it ends here and now, rather than carrying on
    /// behind the set-up-again screen.
    private func stopped(_ words: String) {
        phone.stop()
        state = .setUpAgain(words)
    }

    /// signOut forgets this account's key and certificate. Stopping the
    /// phone at Linx's end is My phones → "I've lost it" in the web app,
    /// which is what to use for a phone someone else has. Every other
    /// account on this phone is untouched.
    func signOut(from button: String) async {
        SignInTrail.signedOut(from: button)
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
