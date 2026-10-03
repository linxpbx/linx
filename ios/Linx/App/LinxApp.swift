import SwiftUI

/// Linx for iPhone and iPad. One window group; the app is portrait-first on the
/// phone and adapts on iPad (build-order step 8 adds the iPad and fold layouts).
@main
struct LinxApp: App {
    var body: some Scene {
        WindowGroup {
            RootView()
        }
    }
}

/// The first thing the app shows. Until a phone is set up (build-order step 4),
/// that is always the setup screen.
struct RootView: View {
    var body: some View {
        switch Screen.launched {
        case .setUpPhone:
            SetUpPhoneView()
        }
    }
}

/// A screen the screenshot harness can start the app on
/// (`ios/tools/screens.sh`, which passes `-LinxScreen <name>`).
/// Debug builds only; a release build always starts at the beginning.
enum Screen: String {
    case setUpPhone = "setup-phone"

    static var launched: Screen {
        #if DEBUG
            let name = UserDefaults.standard.string(forKey: "LinxScreen") ?? ""
            return Screen(rawValue: name) ?? .setUpPhone
        #else
            return .setUpPhone
        #endif
    }
}

#Preview {
    RootView()
}
