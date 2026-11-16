import AgentMuxKit
import SwiftUI

struct SignInView: View {
    @Environment(AppModel.self) private var model
    let link: HubLink
    @State private var login = ""
    @State private var password = ""

    var body: some View {
        Form {
            Section {
                LabeledContent("Hub", value: link.url.absoluteString)
                if let fingerprint = link.fingerprint {
                    LabeledContent("Certificate pinned", value: String(fingerprint.prefix(16)) + "…")
                        .font(.footnote)
                }
            }
            Section {
                TextField("Login name", text: $login)
                    .textContentType(.username)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                SecureField("Password", text: $password)
                    .textContentType(.password)
                    .onSubmit(signIn)
            }
            Section {
                Button("Sign in", action: signIn)
                    .disabled(model.busy || login.trimmingCharacters(in: .whitespaces).isEmpty || password.isEmpty)
                if model.busy { ProgressView() }
            }
            Section {
                Button("Use another hub", role: .destructive) { model.forgetHub() }
            }
        }
        .navigationTitle("Sign in")
    }

    private func signIn() {
        model.signIn(login: login, password: password)
    }
}
