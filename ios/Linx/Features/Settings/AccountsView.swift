import SwiftUI

/// Settings → Accounts (docs/MULTI_ACCOUNT.md §7): the Linx servers this
/// phone is set up on, and the way to add, rename or remove one.
///
/// They are all live at once. Nothing here switches between them — every
/// account rings whether or not this screen is open — so the only choice on
/// it is which one a **new call** is made from, and that is the keypad's
/// "Call with" as much as it is this list's tick.
struct AccountsView: View {
    @Environment(AppModel.self) private var model

    /// The empty account **Add account** opened, while its setup screen is
    /// up. Closing the screen without finishing drops it again.
    @State private var adding: Account?
    @State private var renaming: Account?
    /// The account just added, being named for the first time.
    @State private var naming: Account?
    @State private var newName = ""

    @State private var removing: Account?

    /// What the account would be called if the person types nothing: its
    /// Linx's own address (`AppModel.nameTheAccounts`).
    private var suggestedName: String {
        naming?.identity?.server.host() ?? "its Linx"
    }

    var body: some View {
        List {
            Section {
                ForEach(model.accounts) { account in
                    row(account)
                }
            } footer: {
                Text(
                    "Every account is live at once: they all ring, and you never have to switch between them. A call tells you which account it came through."
                )
            }

            Section {
                Button("Add account") { adding = model.addAnAccount() }
                    .foregroundStyle(LinxColor.accent)
                    .disabled(!model.canAddAnAccount)
            } footer: {
                Text(
                    model.canAddAnAccount
                        ? "You'll need a setup code from the other Linx: My account → My phones → Add phone."
                        : "This phone is set up on \(PhoneAccounts.most) Linx servers, which is as many as it holds."
                )
            }
        }
        .navigationTitle("Accounts")
        .navigationBarTitleDisplayMode(.inline)
        .sheet(item: $adding) { account in
            NavigationStack {
                SetUpPhoneView(account: account)
                    .navigationTitle("Add account")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("Cancel") { adding = nil }
                        }
                    }
            }
            // The moment it is really set up, the sheet has done its job —
            // and the account is named there and then, rather than leaving
            // the person to find the rename later (owner, 2026-10-19).
            .onChange(of: account.state == .signedIn) { _, signedIn in
                guard signedIn else { return }
                adding = nil
                newName = ""
                naming = account
            }
        }
        .onChange(of: adding == nil) { _, closed in
            // Closed without finishing: the empty account goes with it.
            guard closed else { return }
            for account in model.accounts where account.accountID == nil && model.accounts.count > 1 {
                model.giveUpOnAddingAnAccount(account)
            }
        }
        .alert(
            "Name this account",
            isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })
        ) {
            TextField("Work", text: $newName)
            Button("Save") {
                if let account = renaming { model.name(account, newName) }
                renaming = nil
            }
            Button("Cancel", role: .cancel) { renaming = nil }
        } message: {
            Text("What this account is called on this phone — on the lock screen, and beside its calls.")
        }
        // Asked once, as an account is added. Leaving it blank is fine:
        // the account is then called after its Linx.
        .alert(
            "Name this account",
            isPresented: Binding(get: { naming != nil }, set: { if !$0 { naming = nil } })
        ) {
            TextField(suggestedName, text: $newName)
            Button("Save") {
                if let account = naming { model.name(account, newName) }
                naming = nil
            }
            Button("Not now", role: .cancel) { naming = nil }
        } message: {
            Text(
                "What to call it on this phone — on the lock screen, and beside its calls. Leave it blank and it's called after its Linx, \(suggestedName)."
            )
        }
        .confirmationDialog(
            "Remove this account?",
            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
            titleVisibility: .visible
        ) {
            Button("Remove", role: .destructive) {
                if let account = removing {
                    Task { await model.signOut(account, from: "Settings → Accounts → Remove") }
                }
                removing = nil
            }
            Button("Keep it", role: .cancel) { removing = nil }
        } message: {
            Text(
                "This phone stops ringing for that account and forgets its key. Your other accounts are untouched. If you've lost the phone, stop it in Linx on a computer instead."
            )
        }
    }

    private func row(_ account: Account) -> some View {
        Button {
            model.prefer(account)
        } label: {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(name(of: account))
                        .foregroundStyle(LinxColor.text)
                    Text(underneath(account))
                        .font(.footnote)
                        .foregroundStyle(LinxColor.textMuted)
                }
                Spacer()
                if account === model.current, model.hasSeveralAccounts {
                    // Not "the account you are in": the one a new call goes
                    // out on, which is all choosing an account ever means.
                    Image(systemName: "checkmark")
                        .foregroundStyle(LinxColor.accent)
                        .accessibilityLabel("Calls are made from this account")
                }
            }
        }
        .swipeActions {
            Button("Remove", systemImage: "trash", role: .destructive) { removing = account }
                .tint(LinxColor.end)
            Button("Name", systemImage: "pencil") {
                newName = model.typedName(for: account) ?? ""
                renaming = account
            }
            .tint(LinxColor.brandFill)
        }
    }

    /// What the account is called: the name the person gave it, else the
    /// Linx server it is on. The **server** rather than the person, because
    /// the usual second account is the same person on another system, and
    /// two rows both reading "Sara Haddad" tell nobody anything.
    private func name(of account: Account) -> String {
        if let typed = model.typedName(for: account), !typed.isEmpty { return typed }
        guard let identity = account.identity else { return "Not set up yet" }
        // The same name the lock screen and the row labels use, so two
        // accounts on one Linx are told apart here too (`nameTheAccounts`).
        return account.label ?? identity.server.host() ?? identity.server.absoluteString
    }

    /// The line under it: who it signs in as, the extension, and whether
    /// the line is up — the three things that say an account is working.
    private func underneath(_ account: Account) -> String {
        guard let identity = account.identity else {
            if case .setUpAgain = account.state { return "Set this account up again" }
            return "Waiting for a setup code"
        }
        var words = "\(identity.personName) · \(identity.extensionNumber)"
        // Once it has a name of its own, the server is no longer on the
        // row above and belongs here.
        if model.typedName(for: account)?.isEmpty == false {
            words = "\(identity.server.host() ?? "") · " + words
        }
        switch account.state {
        case .signedIn:
            words += account.phone.status == .ready ? " · Ready" : " · No phone line"
        case .setUpAgain:
            words += " · Set this account up again"
        default:
            words += " · Signing in…"
        }
        return words
    }
}

#Preview {
    NavigationStack { AccountsView() }.environment(AppModel())
}
