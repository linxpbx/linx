import SwiftUI

/// Which Linx account the app is on, once this phone holds more than one
/// (`docs/MULTI_ACCOUNT.md` §7). It is **the app's** account, not just the
/// line the next call goes out on: the keypad and the Team tab show the same
/// chooser and switching it on either one switches both (owner, 2026-10-20,
/// "if I am switching from that screen it is supposed to switch the team
/// screen as well"). With one account it isn't there at all.
struct AccountChooser: View {
    /// Optional on purpose: drawn on its own by a renderer or a preview,
    /// with no app around it, it simply draws nothing.
    @Environment(AppModel.self) private var model: AppModel?
    /// The words before the name: "Call with" over the keypad, "Showing"
    /// over the Team list.
    let prefix: String

    private func choice(_ model: AppModel) -> Binding<UUID> {
        Binding(
            get: { model.current.id },
            set: { id in
                if let account = model.accounts.first(where: { $0.id == id }) { model.prefer(account) }
            }
        )
    }

    var body: some View {
        if let model, model.hasSeveralAccounts {
            Menu {
                // A picker, not buttons of our own: iOS lays its rows out
                // itself, so every name starts in the same place and the
                // tick sits where it does everywhere else on the system.
                // Hand-made rows gave the chosen account a Label (an icon
                // and a title) and the rest a bare Text, and iOS lines
                // those two up differently -- the unticked names sat left
                // of the ticked one, with a wide gap after the tick
                // (owner, 2026-10-19).
                Picker(prefix, selection: choice(model)) {
                    ForEach(model.accounts.filter { $0.identity != nil }) { account in
                        Text(account.label ?? "Linx").tag(account.id)
                    }
                }
                .pickerStyle(.inline)
            } label: {
                HStack(spacing: LinxSpace.s1) {
                    Text(prefix)
                        .foregroundStyle(LinxColor.textMuted)
                    Text(model.current.label ?? "Linx")
                        .foregroundStyle(LinxColor.accent)
                    Image(systemName: "chevron.down")
                        .font(.caption2)
                        .foregroundStyle(LinxColor.accent)
                }
                .font(.subheadline)
            }
            .accessibilityLabel("\(prefix) \(model.current.label ?? "Linx"). Change the account the app is on.")
        }
    }
}
