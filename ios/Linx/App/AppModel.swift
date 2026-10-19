import Foundation
import SwiftUI

// What the app is doing, for the screens to follow (docs/PHASE2.md §4,
// docs/MULTI_ACCOUNT.md). The whole app has one of these, and it holds **the
// list of accounts** — one phone can be set up on several Linx servers at
// once, and they are all live together, because an account you have to
// switch into is an account that misses calls.
//
// Everything that belongs to one account is in `Account`. What is left here
// is only what is genuinely the app's and not any one account's:
//
//   - the list, and which account a new call is made from,
//   - Apple's push, which gives the app **one** token for all of them, and
//     the routing that decides which account a push belongs to,
//   - iOS's notification question, which is asked once for the whole app.
//
// With one account every forwarding member below is that account, and the
// app behaves exactly as it did before there could be more than one.

@MainActor @Observable final class AppModel {
    typealias State = Account.State

    /// The app's one model. A push can arrive before any screen exists, so
    /// the delegate and the views have to be looking at the same one
    /// (`AppDelegate`).
    static let shared = AppModel()

    /// Every account this phone holds, oldest first — all of them live.
    private(set) var accounts: [Account]
    /// Which account a new call is made from, and whose screens are shown
    /// where only one account's can be. Always one of `accounts`.
    var current: Account {
        didSet { remember(current) }
    }

    private let push: PushService
    private let keychain: any PhoneStorage
    /// How a new account's session is made, so a test can hand in its own.
    private let makeSession: @MainActor (UUID?) -> PhoneSession

    init(
        push: PushService = .shared,
        keychain: any PhoneStorage = Keychain.shared,
        session: @escaping @MainActor (UUID?) -> PhoneSession = { PhoneSession(account: $0) }
    ) {
        self.push = push
        self.keychain = keychain
        self.makeSession = session
        let account = Account(session: session(nil))
        self.accounts = [account]
        self.current = account
        account.onSignedIn = { [weak self] in await self?.askAboutNotifications() }
    }

    // MARK: - The list (docs/MULTI_ACCOUNT.md §4)

    /// Whether another account may be added: four is the most (§2).
    var canAddAnAccount: Bool { accounts.count < PhoneAccounts.most }
    /// True once this phone really holds more than one account, which is
    /// what every label, section and chooser in the app is gated on: with
    /// one account none of them appears at all (§7). It counts the accounts
    /// the phone **holds**, not the ones signed in this moment, so a label
    /// doesn't come and go as an account signs itself in.
    var hasSeveralAccounts: Bool { accounts.filter { $0.accountID != nil }.count > 1 }

    /// prefer makes an account the one new calls are made from, and
    /// remembers it for next time.
    func prefer(_ account: Account) {
        guard accounts.contains(where: { $0 === account }) else { return }
        current = account
    }

    private func remember(_ account: Account) {
        guard let id = account.accountID else { return }
        var list = PhoneStore.accounts(in: keychain)
        list.prefer(id)
        PhoneStore.save(list, to: keychain)
    }

    /// account finds the account Linx's device id belongs to, which is how
    /// a push says which account it is for (§5).
    func account(id: UUID) -> Account? {
        accounts.first { $0.accountID == id }
    }

    /// addAnAccount puts an empty account at the end and makes it the
    /// current one, so the setup screen sets *it* up. It does nothing at
    /// the limit, and nothing if there is already an empty one waiting.
    @discardableResult func addAnAccount() -> Account? {
        // An account that is already on this phone but couldn't be read —
        // its certificate is gone, so it shows "set it up again" — is still
        // one of its accounts and not a free slot. Only an account that has
        // never been set up at all is one of those.
        if let waiting = accounts.first(where: { $0.accountID == nil }) {
            current = waiting
            return waiting
        }
        guard canAddAnAccount else { return nil }
        let account = Account(session: makeSession(nil))
        account.onSignedIn = { [weak self] in await self?.askAboutNotifications() }
        accounts.append(account)
        current = account
        return account
    }

    // MARK: - Calls to a sleeping phone (docs/PHASE2.md §5, build step 6)

    /// listenForCalls runs once, at the very first moment of the app's life
    /// (`AppDelegate`): PushKit only delivers a call to an app that was
    /// already listening for one.
    func listenForCalls() {
        push.onCall = { [weak self] _, from, video, done in
            Task { @MainActor in
                await self?.woken(from: from, video: video)
                // Apple's "I'm finished with this push", once the phone is
                // ringing. Opening the line goes on in the background.
                done()
            }
        }
        push.onNothingToRing = { [weak self] done in
            Task { @MainActor in
                await self?.phone.wokenByNothing()
                done()
            }
        }
        push.onTokens = { [weak self] tokens in
            Task { @MainActor in await self?.tellLinxWhereToReachThisPhone(tokens) }
        }
        push.start()
    }

    /// woken is a call arriving at a phone whose app was asleep or closed.
    /// Apple gives the app one push token, so the push itself says which
    /// account it is for; a push that doesn't (a Linx older than this
    /// release) rings the current account, which is the right answer for
    /// every phone with one and the only honest one for the rest (§5).
    func woken(device: UUID? = nil, from: String, video: Bool = false) async {
        let account = device.flatMap { self.account(id: $0) } ?? current
        await account.woken(from: from, video: video)
    }

    /// askAboutNotifications asks iOS, once, for the quiet notifications: a
    /// missed call and a new voicemail. It does nothing at all unless this
    /// phone is signed in and the app is in front, and nothing a second
    /// time, so it is safe to call whenever either of those becomes true.
    func askAboutNotifications() async {
        // The bench's phones are never asked: iOS's question over a call
        // keeps the app from being active, which no real call meets twice.
        guard accounts.contains(where: { $0.state == .signedIn }), !Bench.on else { return }
        await push.askAboutNotifications()
        if let tokens = push.current { await tellLinxWhereToReachThisPhone(tokens) }
    }

    /// tellLinxWhereToReachThisPhone sends Apple's tokens on to Linx. There
    /// is one token for the app, so **every** account's server is told it:
    /// each of them has to be able to ring this phone, and each knows the
    /// phone by its own device id (§5, §8).
    func tellLinxWhereToReachThisPhone(_ tokens: PushTokens) async {
        for account in accounts {
            await account.tellLinxWhereToReachThisPhone(tokens)
        }
    }

    // MARK: - Starting up

    /// start runs once, as the app opens. Every account this phone holds
    /// signs itself in, at the same time: they are all live, and one of
    /// them waiting on a slow server must not hold the others up.
    func start() async {
        restoreTheList()
        // Each one is started on its own and then waited for together, so
        // an account on a slow or unreachable server doesn't hold the
        // others up. They all run on the main actor, as they always did.
        let starting = accounts.map { account in Task { @MainActor in await account.start() } }
        for task in starting { await task.value }
    }

    /// restoreTheList makes an `Account` for each enrolment in the Keychain
    /// and picks up the one calls were last made from. A phone that has
    /// never been set up — and one whose Keychain can't be read yet — keeps
    /// the single empty account it started with, which is the setup screen.
    private func restoreTheList() {
        let stored = PhoneStore.accounts(in: keychain)
        guard !stored.isEmpty else { return }
        let existing = accounts
        accounts = stored.ids.map { id in
            if let already = existing.first(where: { $0.accountID == id }) { return already }
            let account = Account(session: makeSession(id), storedAs: id)
            account.onSignedIn = { [weak self] in await self?.askAboutNotifications() }
            return account
        }
        // `current` has a didSet that writes the choice back; this is only
        // reading what was already chosen, so it is set without it.
        if let wanted = stored.current, let account = accounts.first(where: { $0.accountID == wanted }) {
            current = account
        } else if !accounts.contains(where: { $0 === current }) {
            current = accounts[0]
        }
    }

    // MARK: - The current account, for the screens

    /// Until step 5's screens, every screen in the app is the current
    /// account's, and these are what it reads. With one account they are
    /// that account, which is exactly how the app behaved before.
    var state: State {
        get { current.state }
        set { current.state = newValue }
    }
    var identity: PhoneIdentity? {
        get { current.identity }
        set { current.identity = newValue }
    }
    var line: PhoneLine? {
        get { current.line }
        set { current.line = newValue }
    }
    var problem: String? {
        get { current.problem }
        set { current.problem = newValue }
    }
    var phone: PhoneModel { current.phone }
    var home: HomeModel { current.home }
    var linx: LinxAccess? { current.linx }

    func setUp(with code: SetupCode) async { await current.setUp(with: code) }
    func signIn() async { await current.signIn() }

    /// signOut forgets this account on this phone. The account itself is
    /// emptied either way — which is exactly what the app did when there
    /// could only be one — and it leaves the list only if there is another
    /// one left to show.
    func signOut(from button: String) async {
        let account = current
        await account.signOut(from: button)
        guard accounts.count > 1 else { return }
        accounts.removeAll { $0 === account }
        current = accounts[0]
    }
}
