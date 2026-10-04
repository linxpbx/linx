import SwiftUI

/// What a set-up phone shows: whether the line is up, the keypad, and a way
/// to the details of this phone. The Calls, Team and More tabs are
/// build-order step 7 (`docs/PHASE2.md` §12) and aren't shown before they
/// work — App Review refuses an app with placeholder tabs (§14).
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @Environment(PhoneModel.self) private var phone
    /// Open from the start only for the screenshot that shows it
    /// (`ios/tools/screens.sh`); always closed in a release build.
    @State private var showThisPhone = Screen.launched == .thisPhone

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                LineStatusPill(extensionNumber: model.identity?.extensionNumber ?? "")
                Spacer()
                Button {
                    showThisPhone = true
                } label: {
                    Image(systemName: "person.crop.circle")
                        .font(.title2)
                        .foregroundStyle(LinxColor.textMuted)
                }
                .accessibilityLabel("This phone")
            }
            .padding(.horizontal, LinxSpace.s6)
            .padding(.top, LinxSpace.s5)

            KeypadView()
        }
        .linxBackground()
        .sheet(isPresented: $showThisPhone) {
            NavigationStack {
                SignedInView()
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { showThisPhone = false }
                        }
                    }
            }
        }
    }
}

#Preview("Home") {
    HomeView()
        .environment(AppModel())
        .environment(PhoneModel(line: { nil }))
}
