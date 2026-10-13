import SwiftUI

/// Message ids whose state name says it all (as on the status page).
private let quiet: Set<String> = ["not_connected", "disconnected", "connecting", "connected", "disconnecting", "setup_needed"]
private let fixes = ["password": "main.update_password", "totp": "main.update_totp", "repair": "main.repair"]

/// Role model Tailscale's iOS app: the name in a large title with settings in the corner, one
/// card with the state and the switch above everything, the list below (the websites that go
/// through the university, where Tailscale lists the devices).
struct HomeView: View {
    @Bindable var model: AppModel

    var body: some View {
        let status = model.status ?? JSON([:])
        let vpnOn = model.vpn.isOn
        let state = status.string("state")
        let action = status.string("action")
        let fix = fixes[action]
        let stopped = vpnOn && ["auth_failed", "keyring", "error"].contains(state) && !status.bool("busy")
        List {
            Section {
                HStack(spacing: 14) {
                    Circle()
                        .fill(vpnOn ? Look.color(state) : Color.gray)
                        .frame(width: 12, height: 12)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(vpnOn ? model.t("look.\(state)") : model.t("phone.off"))
                            .font(.title3.weight(.semibold))
                        let line = hint(status, vpnOn: vpnOn, state: state)
                        if !line.isEmpty {
                            Text(line)
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        }
                    }
                    Spacer(minLength: 8)
                    Toggle("Uni VPN", isOn: $model.vpnSwitch)
                        .labelsHidden()
                }
                .padding(.vertical, 8)
                if vpnOn && (fix != nil || stopped) {
                    HStack(spacing: 8) {
                        Spacer()
                        if stopped {
                            Button(model.t("main.try_again")) {
                                Task { _ = await model.post("/api/connect") }
                            }
                            .buttonStyle(.bordered)
                        }
                        if let fix {
                            Button(model.t(fix)) {
                                switch action {
                                case "password":
                                    model.passwordDialog = true
                                    model.open(.settings)
                                case "totp":
                                    model.open(.secondFactor)
                                default:
                                    Task { await model.repair() }
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .disabled(status.bool("repairing"))
                        }
                    }
                }
            }
            Section(model.t("settings.through")) {
                ForEach(status.strings("domains"), id: \.self) { host in
                    Label(host, systemImage: "globe")
                }
                Button {
                    model.open(.websites)
                } label: {
                    Label(model.t("phone.edit_websites"), systemImage: "plus")
                }
            }
        }
        .navigationTitle("Uni VPN")
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    model.open(.settings)
                } label: {
                    Image(systemName: "gearshape")
                }
                .accessibilityLabel(model.t("settings.title"))
            }
        }
    }

    private func hint(_ status: JSON, vpnOn: Bool, state: String) -> String {
        // Tapping the switch asks iOS again.
        if !vpnOn { return model.vpnRefused ? model.t("phone.vpn_not_allowed") : "" }
        if !quiet.contains(status.string("message_id")) {
            return model.strings.message(status.string("message"), status.object("message_t"))
        }
        if state == "idle" { return model.t("main.hint.idle") }
        if state == "connected" { return model.t("main.hint.connected") }
        return ""
    }
}
