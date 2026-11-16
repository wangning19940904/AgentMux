import SwiftUI

struct ConnectView: View {
    @Environment(AppModel.self) private var model
    @State private var pasted = ""
    @State private var scanning = false

    var body: some View {
        Form {
            Section {
                Text("Open the hub's Console on a computer and show the phone link, then scan its QR code.")
                Button {
                    scanning = true
                } label: {
                    Label("Scan QR code", systemImage: "qrcode.viewfinder")
                }
                .disabled(!QRScannerView.isAvailable)
                if !QRScannerView.isAvailable {
                    Text("This device cannot scan codes. Paste the link instead.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            Section("Or paste the link") {
                TextField("agentmux://hub?url=…", text: $pasted, axis: .vertical)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.callout.monospaced())
                Button("Connect") { model.useLink(pasted) }
                    .disabled(pasted.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .navigationTitle("AgentMux")
        .sheet(isPresented: $scanning) {
            QRScannerView { code in
                scanning = false
                model.useLink(code)
            }
            .ignoresSafeArea()
        }
    }
}
