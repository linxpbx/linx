import Foundation
import Testing

@testable import Linx

// One call provider, several accounts (docs/MULTI_ACCOUNT.md §6, build step
// 3). iOS wants one `CXProvider` per app, so every account's phone line
// works through the same one and each call is handed back to the account
// whose call it is, by the id iOS already gives it.

@MainActor
@Suite("One call provider, several accounts")
struct SystemCallsRouterTests {
    /// A router over a stand-in system, with two accounts' lines.
    private func two() -> (FakeSystemCalls, SystemCallsRouter, SystemCallsLine, SystemCallsLine) {
        let system = FakeSystemCalls()
        let router = SystemCallsRouter(system: system)
        return (system, router, router.line(), router.line())
    }

    @Test("a call comes back to the account whose call it is")
    func calledBack() async {
        let (system, _, work, staging) = two()
        var atWork: [SystemCallRequest] = []
        var atStaging: [SystemCallRequest] = []
        work.onRequest = { atWork.append($0) }
        staging.onRequest = { atStaging.append($0) }

        let theirs = UUID()
        _ = await staging.reportIncoming(id: theirs, from: SIPPeer(name: "Sara", number: "101"), video: false)
        system.onRequest?(.answer(theirs))
        #expect(atStaging == [.answer(theirs)])
        #expect(atWork.isEmpty)

        // And the other way round, on a call this phone places.
        let mine = UUID()
        work.ask(.start(mine, SIPPeer(name: "Omar", number: "1024")))
        #expect(atWork == [.start(mine, SIPPeer(name: "Omar", number: "1024"))])
        #expect(atStaging.count == 1)
    }

    @Test("both accounts can have a call at the same moment")
    func twoAtOnce() async {
        let (system, _, work, staging) = two()
        var ended: [UUID] = []
        work.onRequest = { if case .end(let id) = $0 { ended.append(id) } }
        staging.onRequest = { if case .end(let id) = $0 { ended.append(id) } }

        let first = UUID(), second = UUID()
        #expect(await work.reportIncoming(id: first, from: SIPPeer(name: "A", number: "1"), video: false))
        #expect(await staging.reportIncoming(id: second, from: SIPPeer(name: "B", number: "2"), video: false))
        #expect(system.reported.count == 2)

        // Each one is hung up on its own account and nowhere else.
        system.onRequest?(.end(second))
        #expect(ended == [second])
    }

    @Test("the sound goes to the call it belongs to")
    func theSound() async {
        let (system, _, work, staging) = two()
        var soundAtWork: [Bool] = []
        var soundAtStaging: [Bool] = []
        work.onAudio = { soundAtWork.append($0) }
        staging.onAudio = { soundAtStaging.append($0) }

        let theirs = UUID()
        _ = await staging.reportIncoming(id: theirs, from: SIPPeer(name: "Sara", number: "101"), video: false)
        // The system hands the microphone over without naming a call; it
        // can only be the one that was just answered.
        system.onRequest?(.answer(theirs))
        system.onAudio?(true)
        #expect(soundAtStaging == [true])
        #expect(soundAtWork.isEmpty)

        // The call ends and the other account places one: the sound follows.
        staging.reportEnded(id: theirs, .hungUp)
        let mine = UUID()
        work.ask(.start(mine, SIPPeer(name: "Omar", number: "1024")))
        system.onAudio?(true)
        #expect(soundAtWork == [true])
        #expect(soundAtStaging == [true])
    }

    @Test("the account's name shares the one line of text, and only when there is more than one")
    func theNameOnTheLockScreen() async {
        let (system, _, work, staging) = two()
        // One account: nothing is added at all, so a call reads exactly as
        // it always has.
        _ = await work.reportIncoming(id: UUID(), from: SIPPeer(name: "Sara Haddad", number: "101"), video: false)
        #expect(system.reported.last?.peer.name == "Sara Haddad")

        staging.accountLabel = "Work"
        _ = await staging.reportIncoming(id: UUID(), from: SIPPeer(name: "Sara Haddad", number: "101"), video: false)
        #expect(system.reported.last?.peer.name == "Sara Haddad — Work")
        // The number is untouched, so the system still knows what it is.
        #expect(system.reported.last?.peer.number == "101")

        // A caller with no name at all is their number and the account.
        _ = await staging.reportIncoming(id: UUID(), from: SIPPeer(name: "", number: "042144444"), video: false)
        #expect(system.reported.last?.peer.name == "042144444 — Work")
        // A withheld number on a named account is the account alone.
        _ = await staging.reportIncoming(id: UUID(), from: SIPPeer(name: "", number: ""), video: false)
        #expect(system.reported.last?.peer.name == "Work")
    }

    @Test("a call the system refuses belongs to nobody afterwards")
    func refused() async {
        let (system, _, work, _) = two()
        system.takesCalls = false
        var atWork: [SystemCallRequest] = []
        work.onRequest = { atWork.append($0) }

        let id = UUID()
        #expect(!(await work.reportIncoming(id: id, from: SIPPeer(name: "A", number: "1"), video: false)))
        // The system never took it, so anything about it afterwards is not
        // this account's business — and the system is told it is over, so it
        // can't be left on the lock screen.
        system.onRequest?(.answer(id))
        #expect(atWork.isEmpty)
        #expect(system.ended.last?.id == id)
        #expect(system.ended.last?.ending == .failed)
    }

    @Test("a system reset reaches every account")
    func reset() async {
        let (system, _, work, staging) = two()
        var forgotten = 0
        work.onReset = { forgotten += 1 }
        staging.onReset = { forgotten += 1 }

        _ = await work.reportIncoming(id: UUID(), from: SIPPeer(name: "A", number: "1"), video: false)
        system.forgetEverything()
        #expect(forgotten == 2)
    }

    @Test("the Phone app setting is the app's, not an account's")
    func recentsIsTheApps() {
        let (system, _, work, _) = two()
        work.showCallsInThePhoneApp(false)
        #expect(system.inPhoneApp == false)
    }
}
