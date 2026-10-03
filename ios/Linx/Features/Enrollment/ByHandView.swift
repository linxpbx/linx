import SwiftUI

/// "Set it up by hand": the server's address and the 8 characters, typed
/// (docs/PHASE2.md §4). For a phone whose camera can't see the code, and for
/// anyone who would rather type.
struct ByHandView: View {
    /// Called with a code that looks right; Linx has the last word on it.
    let onCode: (SetupCode) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var address = ""
    @State private var code = ""
    @FocusState private var focus: Field?

    private enum Field { case address, code }

    private var ready: SetupCode? { SetupCode.byHand(server: address, code: code) }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: LinxSpace.s4) {
                    Text("Your admin gives you both. The code works once and lasts 10 minutes.")
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.textMuted)

                    field(
                        "Linx address", text: $address, field: .address,
                        hint: "pbx.your-company.com",
                        keyboard: .URL)
                    field(
                        "Setup code", text: $code, field: .code, hint: "8 characters",
                        keyboard: .asciiCapable)

                    Button("Set up this phone") {
                        if let code = ready {
                            onCode(code)
                            dismiss()
                        }
                    }
                    .buttonStyle(.linxPrimary)
                    .disabled(ready == nil)
                    .opacity(ready == nil ? 0.5 : 1)
                }
                .padding(LinxSpace.s6)
                .frame(maxWidth: 640, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .linxBackground()
            .navigationTitle("Set it up by hand")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .onAppear { focus = .address }
    }

    private func field(
        _ label: String, text: Binding<String>, field: Field, hint: String, keyboard: UIKeyboardType
    ) -> some View {
        VStack(alignment: .leading, spacing: LinxSpace.s2) {
            Text(label)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(LinxColor.text)
            TextField(hint, text: text)
                .textFieldStyle(.plain)
                .textInputAutocapitalization(field == .code ? .characters : .never)
                .autocorrectionDisabled()
                .keyboardType(keyboard)
                .focused($focus, equals: field)
                .submitLabel(field == .address ? .next : .done)
                .onSubmit { focus = field == .address ? .code : nil }
                .padding(LinxSpace.s4)
                .background(LinxColor.surface, in: .rect(cornerRadius: LinxRadius.md))
                .overlay {
                    RoundedRectangle(cornerRadius: LinxRadius.md).strokeBorder(LinxColor.border)
                }
                .foregroundStyle(LinxColor.text)
        }
    }
}

#Preview {
    ByHandView { _ in }
}
