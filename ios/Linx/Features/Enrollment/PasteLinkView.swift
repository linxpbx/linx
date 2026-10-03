import SwiftUI
import UIKit

/// "Paste setup link": the link from the setup email, on the phone it was
/// sent to (docs/PHASE2.md §4). The link carries the code in its #fragment,
/// so copying it copies the code — and nothing of anyone's password.
struct PasteLinkView: View {
    let onCode: (SetupCode) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var link = ""
    @State private var problem: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: LinxSpace.s4) {
                    Text(
                        "Open the setup email on this phone, hold the link until Copy appears, then come back and paste it here."
                    )
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)

                    TextField("https://pbx.your-company.com/set-up-phone#…", text: $link, axis: .vertical)
                        .textFieldStyle(.plain)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .lineLimit(2...4)
                        .padding(LinxSpace.s4)
                        .background(LinxColor.surface, in: .rect(cornerRadius: LinxRadius.md))
                        .overlay {
                            RoundedRectangle(cornerRadius: LinxRadius.md).strokeBorder(LinxColor.border)
                        }
                        .foregroundStyle(LinxColor.text)

                    Button("Paste") {
                        link = UIPasteboard.general.string ?? ""
                        problem =
                            link.isEmpty ? "There's nothing to paste yet. Copy the link from the email first." : nil
                    }
                    .buttonStyle(.linxSecondary)

                    Button("Set up this phone") { use() }
                        .buttonStyle(.linxPrimary)
                        .disabled(link.isEmpty)
                        .opacity(link.isEmpty ? 0.5 : 1)

                    if let problem {
                        Text(problem)
                            .font(.subheadline)
                            .foregroundStyle(LinxColor.end)
                    }
                }
                .padding(LinxSpace.s6)
                .frame(maxWidth: 640, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .linxBackground()
            .navigationTitle("Paste setup link")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private func use() {
        guard let code = SetupCode.link(link) else {
            problem = "That isn't a Linx setup link. It looks like https://your-linx/set-up-phone#…"
            return
        }
        onCode(code)
        dismiss()
    }
}

#Preview {
    PasteLinkView { _ in }
}
