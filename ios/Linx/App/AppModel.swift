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

    private let session: PhoneSession

    init(session: PhoneSession = PhoneSession()) {
        self.session = session
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
            await phone.start()
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
        await session.forget()
        identity = nil
        line = nil
        problem = nil
        state = .setUp
    }
}
