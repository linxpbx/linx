import SwiftUI

/// Where a call goes while it is up (`docs/PHASE2.md` §12 step 8; ADR-076,
/// the owner's condition).
///
/// On a phone a call covers the screen, as it does in every phone ever
/// made. On a screen with room for it — an iPad, an iPhone Duo opened out —
/// the call **stands beside the app** instead: the list of people stays
/// where it was, the call is in its own column, and taking a call no longer
/// hides everything a person was doing. On a folding phone the join is the
/// fold itself (`Crease`), so the call sits on one leaf and the app on the
/// other, and nothing anybody presses lands in the crease.
extension View {
    func callOnThisScreen(_ phone: PhoneModel) -> some View {
        modifier(CallAlongside(phone: phone))
    }
}

private struct CallAlongside: ViewModifier {
    @Environment(\.horizontalSizeClass) private var horizontal
    @Environment(\.crease) private var crease
    let phone: PhoneModel

    func body(content: Content) -> some View {
        GeometryReader { screen in
            let beside = CallLayout.callFitsBeside(size: screen.size, horizontal: horizontal)
            // The app itself stays in the same place in this layout whether
            // a call is up or not. Moving it between two containers would
            // build it again from scratch every time the phone rang, and
            // take the Team list's websocket and every open screen with it.
            HStack(spacing: 0) {
                content
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                if beside, phone.showsCallScreen, let call = phone.call {
                    Divider()
                    CallView(call: call)
                        .environment(phone)
                        // In its own column, the fold the call has to lay
                        // itself out around is the one across *that* column.
                        .readsTheCrease()
                        .frame(width: CallLayout.callWidth(size: screen.size, crease: crease))
                        .transition(.move(edge: .trailing))
                }
            }
            .frame(width: screen.size.width, height: screen.size.height)
            .animation(.snappy, value: beside && phone.showsCallScreen)
            .fullScreenCover(isPresented: .constant(!beside && phone.showsCallScreen)) {
                if let call = phone.call {
                    CallView(call: call).environment(phone)
                }
            }
        }
    }
}
