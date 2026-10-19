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
    /// The one call provider the whole app shares, with a line of its own
    /// for each account (`SystemCallsRouter`, docs/MULTI_ACCOUNT.md §6).
    @ObservationIgnored private let calls: SystemCallsRouter

    init(
        push: PushService = .shared,
        keychain: any PhoneStorage = Keychain.shared,
        session: @escaping @MainActor (UUID?) -> PhoneSession = { PhoneSession(account: $0) },
        calls: SystemCallsRouter = SystemCallsRouter()
    ) {
        self.push = push
        self.keychain = keychain
        self.makeSession = session
        self.calls = calls
        let account = Account(session: session(nil), calls: calls.line())
        self.accounts = [account]
        self.current = account
        account.onSignedIn = { [weak self] in await self?.askAboutNotifications() }
    }

    /// A fresh account, wired to the app: its own line into the one call
    /// provider, and the app's business handed back up to the app.
    private func newAccount(_ id: UUID?) -> Account {
        let account = Account(session: makeSession(id), storedAs: id, calls: calls.line())
        account.onSignedIn = { [weak self] in await self?.askAboutNotifications() }
        return account
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

    /// addAnAccount puts an empty account at the end for the setup screen
    /// to fill in. It does nothing at the limit, and gives back the empty
    /// one already waiting rather than making a second.
    ///
    /// It deliberately does **not** become the account calls are made from:
    /// that would move the keypad and the screens onto an account that
    /// isn't set up yet. It joins the list properly once it has signed in.
    @discardableResult func addAnAccount() -> Account? {
        // An account that is already on this phone but couldn't be read —
        // its certificate is gone, so it shows "set it up again" — is still
        // one of its accounts and not a free slot. Only an account that has
        // never been set up at all is one of those.
        if let waiting = accounts.first(where: { $0.accountID == nil }) { return waiting }
        guard canAddAnAccount else { return nil }
        let account = newAccount(nil)
        accounts.append(account)
        return account
    }

    /// giveUpOnAddingAnAccount drops an empty account the person closed the
    /// setup screen on. One that was really set up stays, and the app never
    /// drops its last account, which is the setup screen itself.
    func giveUpOnAddingAnAccount(_ account: Account) {
        guard account.accountID == nil, accounts.count > 1 else { return }
        accounts.removeAll { $0 === account }
        if !accounts.contains(where: { $0 === current }) { current = accounts[0] }
    }

    // MARK: - Calls to a sleeping phone (docs/PHASE2.md §5, build step 6)

    /// listenForCalls runs once, at the very first moment of the app's life
    /// (`AppDelegate`): PushKit only delivers a call to an app that was
    /// already listening for one.
    func listenForCalls() {
        push.onCall = { [weak self] _, from, video, device, done in
            Task { @MainActor in
                await self?.woken(device: device, from: from, video: video)
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
        // A push to a phone whose app was closed launches it with nothing
        // set up yet, and this is the first thing that runs — before
        // `start()`. So the list is read out of the Keychain here, or the
        // call would ring on whichever account happened to be first.
        // Reading it is a Keychain read and nothing more; a Keychain that
        // can't be read yet leaves the list as it is, and the call rings on
        // the account calls are made from, which is the honest answer.
        restoreTheList()
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
        await setUpTheBenchsSecondAccount()
    }

    /// The test bench's second account: a phone on two Linx accounts at
    /// once, which is the only way the bench can ring one of them and prove
    /// the other is still live (`ios/tools/bench.sh`, the two-accounts
    /// scenario). Nothing happens in an ordinary run.
    private func setUpTheBenchsSecondAccount() async {
        guard let link = Bench.secondSetupLink, let code = SetupCode.link(link),
            accounts.allSatisfy({ $0.accountID == nil || $0.state == .signedIn }),
            accounts.filter({ $0.accountID != nil }).count == 1,
            let second = addAnAccount()
        else { return }
        await setUp(second, with: code)
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
            return newAccount(id)
        }
        // `current` has a didSet that writes the choice back; this is only
        // reading what was already chosen, so it is set without it.
        if let wanted = stored.current, let account = accounts.first(where: { $0.accountID == wanted }) {
            current = account
        } else if !accounts.contains(where: { $0 === current }) {
            current = accounts[0]
        }
        nameTheAccounts()
        if let first = accounts.first?.accountID { starsBelongToTheFirstAccount?(first) }
    }

    /// Set by the screens: the stars kept on this phone were kept by
    /// extension number alone before there could be more than one account,
    /// and they can only have come from the account this phone already had
    /// (`Favourites.belongTo`).
    @ObservationIgnored var starsBelongToTheFirstAccount: ((UUID) -> Void)?

    /// nameTheAccounts gives each account the name it is shown by where
    /// there is room for one line of text — the lock screen, a car, the
    /// Phone app's Recents (§6, §7). **With one account there is no name at
    /// all**, so an incoming call reads exactly as it always did. The name
    /// is the one the person typed, and otherwise the Linx server's own, as
    /// that is what tells two accounts apart before anybody renames them.
    func nameTheAccounts() {
        guard hasSeveralAccounts else {
            for account in accounts { account.label = nil }
            return
        }
        let stored = PhoneStore.accounts(in: keychain)
        for account in accounts {
            guard let id = account.accountID else {
                account.label = nil
                continue
            }
            account.label = stored.name(for: id) ?? account.identity?.server.host() ?? "Linx"
        }
    }

    // MARK: - The current account, for the screens

    /// Until step 5's screens, every screen in the app is the current
    /// account's, and these are what it reads. With one account they are
    /// that account, which is exactly how the app behaved before.
    /// What the app is doing, which is not quite what one account is
    /// doing: as long as **any** account is signed in the app is usable, so
    /// a second account part-way through being set up, or one that has been
    /// stopped, doesn't take the whole app back to the setup screen. With
    /// one account it is that account, exactly as before.
    var state: State {
        get {
            if accounts.contains(where: { $0.state == .signedIn }) { return .signedIn }
            return current.state
        }
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

    /// Where the merged lists get their rows: every account that is signed
    /// in, with the name to put beside its rows (nil with one account, so
    /// the lists look exactly as they always did). Calls and Voicemail are
    /// one list each, newest first (docs/MULTI_ACCOUNT.md §7).
    var callSources: [CallsModel.Source] {
        accounts.compactMap { account in
            guard let access = account.linx else { return nil }
            return CallsModel.Source(account: account.accountID, label: account.label, access: access)
        }
    }

    /// The two counts on the tab bar, added up across every account: what
    /// somebody missed is what somebody missed, whichever system it came
    /// through (docs/MULTI_ACCOUNT.md §7).
    var missedCalls: Int { accounts.reduce(0) { $0 + $1.home.missedCalls } }
    var newVoicemail: Int { accounts.reduce(0) { $0 + $1.home.newVoicemail } }

    var voicemailSources: [VoicemailModel.Source] {
        accounts.compactMap { account in
            guard let access = account.linx else { return nil }
            return VoicemailModel.Source(account: account.accountID, label: account.label, access: access)
        }
    }
    var home: HomeModel { current.home }
    var linx: LinxAccess? { current.linx }

    func setUp(with code: SetupCode) async { await setUp(current, with: code) }

    /// setUp redeems a code for one account — the current one, or the empty
    /// one that **Add account** opened.
    func setUp(_ account: Account, with code: SetupCode) async {
        await account.setUp(with: code)
        // It may be this phone's second account, which is the moment every
        // label and section in the app starts appearing.
        nameTheAccounts()
    }

    /// name gives an account the name the person typed ("Work"), or takes
    /// it away again, and keeps it with the account in the Keychain.
    func name(_ account: Account, _ name: String) {
        guard let id = account.accountID else { return }
        var list = PhoneStore.accounts(in: keychain)
        list.name(name, for: id)
        PhoneStore.save(list, to: keychain)
        nameTheAccounts()
    }

    /// What an account is shown as in the Accounts list: the name the
    /// person typed, else who it signs in as and where.
    func typedName(for account: Account) -> String? {
        guard let id = account.accountID else { return nil }
        return PhoneStore.accounts(in: keychain).name(for: id)
    }
    func signIn() async { await current.signIn() }

    #if DEBUG
        /// A second account, for the screenshots and the previews: no key,
        /// no certificate and no Linx to talk to (`Screen`).
        func pretendASecondAccount(_ identity: PhoneIdentity) {
            guard let account = addAnAccount() else { return }
            account.identity = identity
            account.label = identity.server.host()
            accounts.first?.label = accounts.first?.identity?.server.host()
        }
    #endif

    /// signOut forgets this account on this phone. The account itself is
    /// emptied either way — which is exactly what the app did when there
    /// could only be one — and it leaves the list only if there is another
    /// one left to show.
    func signOut(from button: String) async { await signOut(current, from: button) }

    /// signOut forgets one account on this phone — the one the person
    /// chose in the Accounts list, or the only one there is.
    func signOut(_ account: Account, from button: String) async {
        await account.signOut(from: button)
        guard accounts.count > 1 else {
            nameTheAccounts()
            return
        }
        accounts.removeAll { $0 === account }
        if !accounts.contains(where: { $0 === current }) { current = accounts[0] }
        nameTheAccounts()
    }
}
