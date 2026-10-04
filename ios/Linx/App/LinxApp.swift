import SwiftUI
import UIKit

/// Linx for iPhone and iPad. One window group; the app is portrait-first on the
/// phone and adapts on iPad (build-order step 8 adds the iPad and fold layouts).
@main
struct LinxApp: App {
    /// A push can arrive before any of this exists, so the delegate is what
    /// registers for one (`AppDelegate`) and it works on the same model.
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = Screen.launched.model()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
        }
    }
}

/// Which screen belongs on the display, which is only ever a question of what
/// the app is doing (`AppModel.State`).
struct RootView: View {
    @Environment(AppModel.self) private var model

    @Environment(\.scenePhase) private var scenePhase
    /// Light, dark or the phone's own setting (Settings → Appearance).
    @AppStorage(Settings.appearance) private var appearance = Appearance.system.rawValue

    var body: some View {
        Group {
            switch model.state {
            case .setUp:
                SetUpPhoneView()
            case .working(let what):
                WorkingView(what: what)
            case .signedIn:
                HomeView()
            case .setUpAgain(let reason):
                SetUpAgainView(reason: reason)
            }
        }
        .environment(model.phone)
        .preferredColorScheme(Appearance(rawValue: appearance)?.scheme)
        .fullScreenCover(isPresented: .constant(model.phone.call != nil)) {
            if let call = model.phone.call {
                CallView(call: call).environment(model.phone)
            }
        }
        .onChange(of: scenePhase) { _, phase in
            // Nothing runs in the background: the websocket lives only while
            // the app is in front, and a push wakes it when a call comes
            // (docs/PHASE2.md §7, §5). A call in progress keeps its line, and
            // so does a phone that a push has just woken and whose call is
            // still on its way.
            // A screenshot run talks to nothing at all (`Screen`).
            guard Screen.launched == nil, case .signedIn = model.state, !model.phone.busy
            else { return }
            switch phase {
            case .background: model.phone.stop()
            case .active: Task { await model.phone.start() }
            default: break
            }
        }
        .task {
            // A screenshot run is given its state up front and talks to
            // nothing; everything else picks up where it left off.
            if Screen.launched == nil {
                await model.start()
            } else {
                Screen.turnTheScreenForTheShot()
            }
        }
    }
}

/// Setting up, or signing in again: one line and a spinner, because both take
/// a second or two at most.
struct WorkingView: View {
    let what: String

    var body: some View {
        VStack(spacing: LinxSpace.s4) {
            ProgressView()
            Text(what)
                .font(.body)
                .foregroundStyle(LinxColor.textMuted)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .linxBackground()
    }
}

/// A screen the screenshot harness can start the app on
/// (`ios/tools/screens.sh`, which passes `-LinxScreen <name>`).
/// Debug builds only; a release build always starts at the beginning and
/// talks to the real Linx.
enum Screen: String {
    case setUpPhone = "setup-phone"
    case signedIn = "signed-in"
    /// The home screen with "This phone" open over it.
    case thisPhone = "this-phone"
    case setUpAgain = "set-up-again"
    case keypad
    case inCall = "in-call"
    case incomingCall = "incoming-call"
    case calls
    case team
    case more
    case voicemail
    case settings
    /// A call with a picture in it, which is the screen that lays itself
    /// out to whatever it is on (`CallLayout`): shoot it on an iPhone
    /// upright and on its side, on an iPad, and on an iPhone Duo folded and
    /// unfolded.
    case videoCall = "video-call"

    /// Which screen behind **More** this screenshot wants open.
    var morePage: MoreView.Page? {
        switch self {
        case .voicemail: return .voicemail
        case .settings: return .settings
        default: return nil
        }
    }

    /// Which tab the app should open on for this screenshot.
    var tab: HomeView.Tabs {
        switch self {
        case .calls: return .calls
        case .team: return .team
        case .more, .voicemail, .settings: return .more
        default: return .keypad
        }
    }

    /// A screenshot run can ask for the phone on its side
    /// (`-LinxOrientation landscape`), which is how the call screens'
    /// landscape layouts are shot without anybody holding a phone. Debug
    /// builds only, and it does nothing in an ordinary run.
    @MainActor static func turnTheScreenForTheShot() {
        #if DEBUG
            guard UserDefaults.standard.string(forKey: "LinxOrientation") == "landscape",
                let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene
            else { return }
            scene.requestGeometryUpdate(.iOS(interfaceOrientations: .landscapeRight))
        #endif
    }

    static var launched: Screen? {
        #if DEBUG
            let name = UserDefaults.standard.string(forKey: "LinxScreen") ?? ""
            return Screen(rawValue: name)
        #else
            return nil
        #endif
    }

    /// model is an app in the state this screen needs, with made-up contents
    /// and no phone set up: a screenshot run never has a key, a certificate
    /// or a Linx to talk to.
    @MainActor func model() -> AppModel {
        let model = AppModel()
        switch self {
        case .setUpPhone:
            break
        case .signedIn, .thisPhone, .keypad, .inCall, .incomingCall, .calls, .team, .more, .voicemail,
            .settings, .videoCall:
            model.identity = Screen.sampleIdentity
            model.line = nil
            model.state = .signedIn
            #if DEBUG
                model.phone.pretend(.ready)
                model.home.pretend(members: Screen.sampleTeam, missed: 2, voicemail: 1)
            #endif
            if self == .keypad {
                model.phone.typed = "+971 4 000 0123"
            }
            #if DEBUG
                if self == .inCall {
                    model.phone.pretend(
                        PhoneModel.Call(
                            peer: SIPPeer(name: "Sara Haddad", number: "1024"), incoming: false,
                            phase: .active, answeredAt: Date(timeIntervalSinceNow: -252),
                            connection: MediaConnection(
                                route: .direct, roundTripMs: 38, relayProtocol: nil, audioBytesIn: 48_000)))
                }
                if self == .incomingCall {
                    model.phone.pretend(
                        PhoneModel.Call(
                            peer: SIPPeer(name: "Omar Nasser", number: "1031"), incoming: true,
                            phase: .ringing))
                }
                if self == .videoCall {
                    // Both cameras on. A screenshot run has no camera and no
                    // call, so the screen shows what it shows in a real call
                    // before the first frame arrives.
                    model.phone.pretend(
                        PhoneModel.Call(
                            peer: SIPPeer(name: "Sara Haddad", number: "1024"), incoming: false,
                            phase: .active, answeredAt: Date(timeIntervalSinceNow: -96),
                            video: CallVideo(mine: true, theirs: true),
                            connection: MediaConnection(
                                route: .direct, roundTripMs: 24, relayProtocol: nil, audioBytesIn: 24_000)))
                }
            #endif
        case .setUpAgain:
            model.identity = Screen.sampleIdentity
            model.state = .setUpAgain("The password of this Linx account changed, so this phone was logged out.")
        }
        return model
    }

    /// A team, a few calls and a message, for the screenshots. None of it
    /// is anybody's: the names are the ones the mockups use.
    static let sampleTeam: [TeamMember] = [
        TeamMember(extensionNumber: "101", name: "Sara Haddad", status: .available, since: nil),
        TeamMember(
            extensionNumber: "1024", name: "Omar Khalil", status: .onCall,
            since: Date(timeIntervalSinceNow: -252)),
        TeamMember(extensionNumber: "1031", name: "Lina Farouk", status: .ringing, since: Date()),
        TeamMember(extensionNumber: "1040", name: "Daniel Reyes", status: .away, since: nil),
        TeamMember(extensionNumber: "1042", name: "Aisha Rahman", status: .dnd, since: nil),
        TeamMember(extensionNumber: "1047", name: "Chen Wei", status: .offline, since: nil),
    ]

    static let sampleCalls: [CallRecord] = [
        CallRecord(
            id: UUID(uuidString: "0199c0de-0000-7000-8000-00000000c001")!, direction: .inbound,
            startedAt: Date(timeIntervalSinceNow: -1_800), answeredAt: nil, talkSeconds: 0, result: "missed",
            missed: true, placedByMe: false, from: CallParty(number: "+971 4 555 0147", name: "Al Noor Trading"),
            to: CallParty(number: "101", name: "Sara Haddad"), ringGroup: nil, answeredBy: nil),
        CallRecord(
            id: UUID(uuidString: "0199c0de-0000-7000-8000-00000000c002")!, direction: .outbound,
            startedAt: Date(timeIntervalSinceNow: -5_400), answeredAt: Date(timeIntervalSinceNow: -5_390),
            talkSeconds: 134, result: "answered", missed: false, placedByMe: true,
            from: CallParty(number: "101", name: "Sara Haddad"),
            to: CallParty(number: "1024", name: "Omar Khalil"), ringGroup: nil, answeredBy: "Omar Khalil (1024)"),
        CallRecord(
            id: UUID(uuidString: "0199c0de-0000-7000-8000-00000000c003")!, direction: .inbound,
            startedAt: Date(timeIntervalSinceNow: -90_000), answeredAt: nil, talkSeconds: 0, result: "voicemail",
            missed: true, placedByMe: false, from: CallParty(number: "+971 50 123 4567", name: ""),
            to: CallParty(number: "101", name: "Sara Haddad"), ringGroup: "Sales", answeredBy: nil),
    ]

    static let sampleVoicemail: [VoicemailMessage] = [
        VoicemailMessage(
            id: UUID(uuidString: "0199c0de-0000-7000-8000-00000000d001")!,
            boxID: UUID(uuidString: "0199c0de-0000-7000-8000-00000000e001")!,
            callerNumber: "+971 50 123 4567", callerName: "", receivedAt: Date(timeIntervalSinceNow: -4_000),
            durationMs: 23_000, heardByMe: false, heardBy: nil),
        VoicemailMessage(
            id: UUID(uuidString: "0199c0de-0000-7000-8000-00000000d002")!,
            boxID: UUID(uuidString: "0199c0de-0000-7000-8000-00000000e001")!,
            callerNumber: "1024", callerName: "Omar Khalil", receivedAt: Date(timeIntervalSinceNow: -180_000),
            durationMs: 9_000, heardByMe: true, heardBy: "Sara Haddad"),
    ]

    static var sampleIdentity: PhoneIdentity {
        PhoneIdentity(
            server: URL(string: "https://pbx.example.com")!,
            deviceID: UUID(uuidString: "0199c0de-0000-7000-8000-000000000001")!,
            deviceName: "Sara's iPhone", personName: "Sara Haddad", extensionNumber: "101",
            certificate: "", ca: "",
            // Six months out, as a phone in use always is.
            certNotAfter: Date(timeIntervalSince1970: 1_806_800_000),
            setUpAgain: Date(timeIntervalSince1970: 1_806_800_000), keyKind: .software)
    }
}

extension Optional where Wrapped == Screen {
    /// A screenshot run with no `-LinxScreen` starts the app for real, on
    /// the one model a push also reaches (`AppModel.shared`).
    @MainActor func model() -> AppModel { self?.model() ?? AppModel.shared }
}

#Preview {
    RootView().environment(AppModel())
}
