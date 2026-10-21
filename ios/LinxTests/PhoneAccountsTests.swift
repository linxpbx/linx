import Foundation
import Testing

@testable import Linx

// More than one account on one phone (docs/MULTI_ACCOUNT.md §4): the list,
// each account's own two slots, and the one-time move of a phone that was
// set up before there could be more than one. Nothing here touches a
// network or a real Keychain.

/// A store that can't be read, the way the Keychain can't be before the
/// phone has been unlocked since it started.
final class LockedStore: PhoneStorage, @unchecked Sendable {
    @discardableResult func save(_ data: Data, as account: String) -> Bool { false }
    func read(_ account: String) -> Data? { nil }
    func delete(_ account: String) { saw = true }
    func status(_ account: String) -> OSStatus { errSecInteractionNotAllowed }
    /// Set if anything was thrown away, which must never happen here.
    private(set) var saw = false
}

/// A phone that has already been moved to accounts, woken before its first
/// unlock: the list can't be read, and the two old slots are genuinely gone.
/// "Can't read it" must not come out as "nothing there".
final class LockedAfterTheMoveStore: PhoneStorage, @unchecked Sendable {
    @discardableResult func save(_ data: Data, as account: String) -> Bool { false }
    func read(_ account: String) -> Data? { nil }
    func delete(_ account: String) {}
    func status(_ account: String) -> OSStatus {
        account == PhoneStore.accountsAccount ? errSecInteractionNotAllowed : errSecItemNotFound
    }
}

@Suite("Accounts on one phone")
struct PhoneAccountsTests {
    private func identity(_ id: UUID, server: String) -> PhoneIdentity {
        PhoneIdentity(
            server: URL(string: server)!, deviceID: id, deviceName: "iPhone", personName: "Sam",
            extensionNumber: "101", certificate: "", ca: "", certNotAfter: .distantFuture,
            setUpAgain: .distantFuture, keyKind: .software)
    }

    @Test("two accounts are kept apart, and removing one leaves the other")
    func twoAccounts() throws {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        var list = PhoneStore.accounts(in: store)
        #expect(list.ids == [work, staging])
        // The first one added is the one a call is made from, until another
        // is chosen.
        #expect(list.current == work)
        list.prefer(staging)
        PhoneStore.save(list, to: store)
        #expect(PhoneStore.accounts(in: store).current == staging)

        PhoneStore.forget(work, from: store)
        let left = PhoneStore.accounts(in: store)
        #expect(left.ids == [staging])
        #expect(left.current == staging)
        #expect(store.read(PhoneStore.identityAccount(work)) == nil)
        #expect(store.read(PhoneStore.identityAccount(staging)) != nil)
    }

    @Test("saving the same account again doesn't add it twice")
    func savedTwice() {
        let store = FakeStore()
        let id = UUID()
        PhoneStore.save(identity(id, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(id, server: "https://pbx.example.com"), key: nil, to: store)
        #expect(PhoneStore.accounts(in: store).ids == [id])
    }

    @Test("a name the person typed belongs to its account and goes with it")
    func names() {
        let store = FakeStore()
        let id = UUID()
        PhoneStore.save(identity(id, server: "https://pbx.example.com"), key: nil, to: store)
        var list = PhoneStore.accounts(in: store)
        list.name("  Work  ", for: id)
        PhoneStore.save(list, to: store)
        #expect(PhoneStore.accounts(in: store).name(for: id) == "Work")

        list.name("   ", for: id)
        #expect(list.name(for: id) == nil)
        list.remove(id)
        #expect(list.isEmpty)
        #expect(list.current == nil)
    }

    @Test("four is the most, and an account this phone hasn't got is ignored")
    func theLimit() {
        var list = PhoneAccounts()
        for _ in 0..<PhoneAccounts.most { list.add(UUID()) }
        #expect(list.isFull)
        let stranger = UUID()
        list.prefer(stranger)
        #expect(list.current == list.ids.first)
    }

    @Test("a phone set up by an older app becomes its first account, once")
    func theOldSlotsMoveIn() throws {
        let store = FakeStore()
        let id = UUID()
        let old = identity(id, server: "https://home.example.com")
        store.save(try JSONEncoder().encode(old), as: PhoneStore.oldIdentityAccount)
        store.save(Data("a key blob".utf8), as: PhoneStore.oldKeyAccount)

        let list = PhoneStore.accounts(in: store)
        #expect(list.ids == [id])
        #expect(store.read(PhoneStore.identityAccount(id)) != nil)
        #expect(store.read(PhoneStore.keyAccount(id)) == Data("a key blob".utf8))
        // And the old slots are gone, so it only ever happens once.
        #expect(store.read(PhoneStore.oldIdentityAccount) == nil)
        #expect(store.read(PhoneStore.oldKeyAccount) == nil)
        #expect(PhoneStore.accounts(in: store).ids == [id])
    }

    @Test("a Keychain that can't be read yet is left completely alone")
    func lockedIsNotEmpty() {
        let store = LockedStore()
        #expect(PhoneStore.accounts(in: store).isEmpty)
        // Nothing was thrown away on the strength of a read that couldn't
        // happen, and the trail says locked rather than missing.
        #expect(!store.saw)
        #expect(PhoneStore.status(nil, in: store).identity == errSecInteractionNotAllowed)
        #expect(PhoneStore.status(UUID(), in: store).key == errSecInteractionNotAllowed)
    }

    @Test("an account whose key is gone isn't half-loaded")
    func halfAnAccount() {
        let store = FakeStore()
        let id = UUID()
        PhoneStore.save(identity(id, server: "https://pbx.example.com"), key: nil, to: store)
        // Saved with no key (a renewal), so there is no key blob to restore.
        #expect(PhoneStore.load(id, from: store) == nil)
        #expect(PhoneStore.status(id, in: store).identity == errSecSuccess)
        #expect(PhoneStore.status(id, in: store).key == errSecItemNotFound)
    }

    @Test("a locked Keychain on a phone already moved to accounts still reads as locked")
    func lockedAfterTheMove() {
        // The old slots are gone, so they answer "not there" however locked
        // the phone is; only the list itself can say what is really going
        // on, and the app must wait for the unlock rather than ask to be
        // set up again (2026-10-19).
        let how = PhoneStore.status(nil, in: LockedAfterTheMoveStore())
        #expect(how.identity == errSecInteractionNotAllowed)
        #expect(PhoneSession.Missing(identity: how.identity, key: how.key).locked)
    }
}

// The app's side of it (docs/MULTI_ACCOUNT.md §3, build step 2): `AppModel`
// holds the list and nothing else, and with one account every screen sees
// exactly what it saw before there could be more than one.

@MainActor
@Suite("The list of accounts")
struct AppAccountsTests {
    private func identity(_ id: UUID, server: String) -> PhoneIdentity {
        PhoneIdentity(
            server: URL(string: server)!, deviceID: id, deviceName: "iPhone", personName: "Sam",
            extensionNumber: "101", certificate: "", ca: "", certNotAfter: .distantFuture,
            setUpAgain: .distantFuture, keyKind: .software)
    }

    /// An app on a store of its own, and no sign-in trail left over from
    /// another test (a phone that was signed out on purpose doesn't stop to
    /// write a note about its missing sign-in).
    private func app(_ store: FakeStore) -> AppModel {
        SignInTrail.signedOut(from: "a test")
        return AppModel(keychain: store, session: { PhoneSession(account: $0, keychain: store) })
    }

    @Test("one account is the whole app, exactly as before")
    func oneAccount() async {
        let model = app(FakeStore())
        #expect(model.accounts.count == 1)
        #expect(!model.hasSeveralAccounts)
        #expect(model.canAddAnAccount)
        // The screens read the app and get the account.
        #expect(model.state == .setUp)
        model.identity = Screen.sampleIdentity
        model.state = .signedIn
        #expect(model.current.identity?.extensionNumber == "101")
        #expect(model.current.state == .signedIn)
        #expect(model.phone === model.current.phone)
        #expect(model.home === model.current.home)
        // Signing out empties the account and keeps it: that is what the
        // app did when there could only ever be one.
        await model.signOut(from: "a test")
        #expect(model.accounts.count == 1)
        #expect(model.identity == nil)
        #expect(model.state == .setUp)
    }

    @Test("every account on the phone comes back, and the chosen one is current")
    func theListComesBack() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)
        var list = PhoneStore.accounts(in: store)
        list.prefer(staging)
        PhoneStore.save(list, to: store)

        let model = app(store)
        await model.start()
        #expect(model.accounts.map(\.accountID) == [work, staging])
        #expect(model.current.accountID == staging)
        // Neither of them could be loaded (no key blob was saved), so both
        // are back at the setup screen — which is the point of the check:
        // the list is the Keychain's, not the sign-in's.
        #expect(model.accounts.allSatisfy { $0.state == .setUp })
    }

    @Test("a push says which account it is for")
    func pushPicksTheAccount() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        let model = app(store)
        await model.start()
        #expect(model.account(id: staging) === model.accounts[1])
        // An id this phone doesn't hold rings nothing of its own: the call
        // falls to the account calls are made from (§5).
        #expect(model.account(id: UUID()) == nil)
    }

    @Test("a push says which account it is for, and the app reads it")
    func thePushNamesTheAccount() {
        let id = UUID()
        let said = PushService.call(in: [
            "aps": [:],
            "linx": [
                "call": "1759500000.7", "from": "+971500000001", "at": 1_759_500_000,
                "video": true, "device": id.uuidString.lowercased(),
            ],
        ])
        #expect(said?.call == "1759500000.7")
        #expect(said?.from == "+971500000001")
        #expect(said?.video == true)
        #expect(said?.device == id)

        // A Linx older than multi-account names no account: the call rings
        // on the one calls are made from, which is right for every phone
        // with one account and the only honest answer for the rest (§5).
        let older = PushService.call(in: ["aps": [:], "linx": ["call": "1759500000.8", "from": "101"]])
        #expect(older?.device == nil)
        #expect(older?.call == "1759500000.8")

        // Nonsense where the account should be is the same as none.
        let rubbish = PushService.call(in: ["aps": [:], "linx": ["call": "1", "device": "not-an-id"]])
        #expect(rubbish?.device == nil)
        // And a push with no call at all is not a Linx call.
        #expect(PushService.call(in: ["aps": [:], "linx": ["device": id.uuidString]]) == nil)
    }

    @Test("a woken phone finds its accounts before it rings one")
    func wokenBeforeTheListIsRead() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        // A push to a closed app launches it with nothing set up yet, and
        // this runs before start() ever does.
        let model = app(store)
        #expect(model.accounts.count == 1)
        await model.woken(device: staging, from: "+971500000001")
        #expect(model.accounts.count == 2)
        #expect(model.account(id: staging) != nil)
    }

    @Test("two accounts on one Linx are told apart by their extension")
    func sameServerTwice() async {
        let store = FakeStore()
        let desk = UUID(), mine = UUID(), elsewhere = UUID()
        // Two extensions of the same company, and one on another Linx.
        var one = identity(desk, server: "https://pbx.example.com")
        one = PhoneIdentity(
            server: one.server, deviceID: desk, deviceName: one.deviceName, personName: "Sam",
            extensionNumber: "101", certificate: "", ca: "", certNotAfter: .distantFuture,
            setUpAgain: .distantFuture, keyKind: .software)
        let two = PhoneIdentity(
            server: URL(string: "https://pbx.example.com")!, deviceID: mine, deviceName: "iPhone",
            personName: "Sam", extensionNumber: "204", certificate: "", ca: "",
            certNotAfter: .distantFuture, setUpAgain: .distantFuture, keyKind: .software)
        PhoneStore.save(one, key: nil, to: store)
        PhoneStore.save(two, key: nil, to: store)
        PhoneStore.save(identity(elsewhere, server: "https://home.example.com"), key: nil, to: store)

        let model = app(store)
        await model.start()
        for (account, stored) in zip(model.accounts, [one, two]) { account.identity = stored }
        model.accounts[2].identity = identity(elsewhere, server: "https://home.example.com")
        model.nameTheAccounts()
        // The two that share a Linx carry their extension; the one that
        // doesn't is named by its server alone, as before.
        #expect(model.accounts[0].label == "pbx.example.com · 101")
        #expect(model.accounts[1].label == "pbx.example.com · 204")
        #expect(model.accounts[2].label == "home.example.com")

        // A name the person typed always wins.
        model.name(model.accounts[1], "Work")
        #expect(model.accounts[1].label == "Work")
        #expect(model.accounts[0].label == "pbx.example.com · 101")
    }

    @Test("an account that signs in after the tabs are up still fills its Team list")
    func everyAccountsTeamIsStarted() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        let model = app(store)
        await model.start()
        // The tabs appear and ask for every account's team.
        model.watchTheTeams()
        #expect(model.accounts.allSatisfy { $0.home.running }, "every account's team is live at once")

        // Signing in finishes afterwards and hands this account a team model
        // of its own. It must be started too — this is what left two of the
        // owner's three accounts loading for ever (2026-10-21).
        model.accounts[1].identity = identity(staging, server: "https://home.example.com")
        model.accounts[1].teamModelFollowsTheDevice()
        #expect(model.accounts[1].home.running, "a team model made later is started as well")

        // An account added while the tabs are up is watched from the start.
        let added = try? #require(model.addAnAccount())
        #expect(added?.home.running == true)

        // Leaving the tabs stops them all, and stops them staying wanted.
        model.watchTheTeams(false)
        #expect(model.accounts.allSatisfy { !$0.home.running })
        model.accounts[1].identity = identity(UUID(), server: "https://home.example.com")
        model.accounts[1].teamModelFollowsTheDevice()
        #expect(!model.accounts[1].home.running, "nothing starts behind a screen nobody is looking at")
    }

    @Test("choosing an account is remembered for next time")
    func theChoiceIsKept() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        let model = app(store)
        await model.start()
        model.prefer(model.accounts[1])
        #expect(PhoneStore.accounts(in: store).current == staging)
        // An account from somewhere else is not this app's to choose.
        model.prefer(Account())
        #expect(model.current.accountID == staging)
    }

    @Test("ringing back from a merged list moves the app onto that row's account")
    func aRowTakesTheAppWithIt() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        let model = app(store)
        await model.start()
        model.prefer(model.accounts[0])
        #expect(model.current.accountID == work)
        // Calls and Voicemail are one list for every account, so ringing
        // somebody back has to take the app with it: afterwards the keypad
        // and the Team tab are on the Linx the call went out on (owner,
        // 2026-10-20).
        model.use(accountID: staging)
        #expect(model.current.accountID == staging)
        // A row with no account of its own — one account on the phone —
        // changes nothing, and nor does one from another phone.
        model.use(accountID: nil)
        #expect(model.current.accountID == staging)
        model.use(accountID: UUID())
        #expect(model.current.accountID == staging)
    }

    @Test("signing out of one account leaves the others alone")
    func signOutOfOne() async {
        let store = FakeStore()
        let work = UUID(), staging = UUID()
        PhoneStore.save(identity(work, server: "https://pbx.example.com"), key: nil, to: store)
        PhoneStore.save(identity(staging, server: "https://home.example.com"), key: nil, to: store)

        let model = app(store)
        await model.start()
        model.prefer(model.accounts[0])
        await model.signOut(from: "a test")
        #expect(model.accounts.count == 1)
        #expect(model.current.accountID == staging)
        #expect(PhoneStore.accounts(in: store).ids == [staging])
    }

    @Test("four accounts is the most, and an empty one is never doubled")
    func theLimit() async {
        let store = FakeStore()
        let ids = (0..<PhoneAccounts.most).map { _ in UUID() }
        for id in ids { PhoneStore.save(identity(id, server: "https://pbx.example.com"), key: nil, to: store) }

        let model = app(store)
        await model.start()
        #expect(model.accounts.count == PhoneAccounts.most)
        #expect(!model.canAddAnAccount)
        #expect(model.hasSeveralAccounts)
        // Full, and none of these is a free slot even though none of them
        // could be read (no key blob was saved): an account the phone holds
        // is an account, whether or not it is signed in this moment.
        #expect(model.addAnAccount() == nil)

        // With room, Add account opens one empty account — and asking twice
        // gives back the one already waiting rather than a second.
        let smaller = app(FakeStore())
        let waiting = smaller.addAnAccount()
        #expect(smaller.accounts.count == 1)
        #expect(waiting === smaller.addAnAccount())

        // An account being set up never becomes the one calls are made
        // from: that would move the keypad onto an account that isn't set
        // up yet. And closing the setup screen on it drops it again.
        let store2 = FakeStore()
        let there = UUID()
        PhoneStore.save(identity(there, server: "https://pbx.example.com"), key: nil, to: store2)
        let two = app(store2)
        await two.start()
        let fresh = try! #require(two.addAnAccount())
        #expect(two.current.accountID == there)
        #expect(two.accounts.count == 2)
        two.giveUpOnAddingAnAccount(fresh)
        #expect(two.accounts.count == 1)
        #expect(two.current.accountID == there)
    }
}
