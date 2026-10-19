import AppKit
import SwiftUI
import VaniApplyKit

struct BrowsersView: View {
    @Environment(AppModel.self) private var model
    @State private var idDraft = ""

    var body: some View {
        Form {
            Section("Safari") {
                LabeledContent("Extension") {
                    switch model.safariEnabled {
                    case .some(true): Label("On", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
                    case .some(false): Label("Off", systemImage: "circle").foregroundStyle(.secondary)
                    case .none: Text(model.safariStateError ?? "Unknown").foregroundStyle(.secondary)
                    }
                }
                HStack {
                    Button("Open Safari Extension Settings") { model.openSafariSettings() }
                    Button("Refresh") { model.refreshSafari() }
                }
                Text("Turn on Vani Apply in Safari ▸ Settings ▸ Extensions and allow it on job sites. A development-signed build may also need Safari ▸ Settings ▸ Developer ▸ “Allow unsigned extensions”, which Safari resets when it quits.")
                    .font(.caption).foregroundStyle(.secondary)
            }

            Section("Chrome") {
                ForEach(ChromeConnector.Browser.allCases, id: \.self) { b in
                    ChromeRow(browser: b, status: model.chromeStatus[b] ?? .notConnected)
                }
                LabeledContent("Extension ID") {
                    HStack {
                        TextField("", text: $idDraft).font(.body.monospaced()).frame(width: 300)
                        Button("Use") { model.extensionID = idDraft }
                            .disabled(!ChromeConnector.isValidExtensionID(idDraft) || idDraft == model.extensionID)
                        Button("Default") { idDraft = ChromeConnector.defaultExtensionID; model.extensionID = idDraft }
                            .disabled(model.extensionID == ChromeConnector.defaultExtensionID)
                    }
                }
                Text("1. In chrome://extensions turn on Developer mode, choose Load unpacked and pick the extension folder. 2. Check that the ID Chrome shows matches the one above. 3. Click Connect. Connecting writes one file, \(ChromeConnector.hostName).json, in Chrome's NativeMessagingHosts folder; Disconnect removes it. Until then the extension keeps answers inside Chrome.")
                    .font(.caption).foregroundStyle(.secondary)
                HStack {
                    Button("Show Extension Folder") { NSWorkspace.shared.activateFileViewerSelecting([model.chromeExtensionFolder]) }
                        .disabled(!FileManager.default.fileExists(atPath: model.chromeExtensionFolder.path))
                    Button("Refresh") { model.refreshChrome() }
                }
            }
        }
        .formStyle(.grouped)
        .onAppear { idDraft = model.extensionID; model.refreshChrome(); model.refreshSafari() }
        .navigationTitle("Browsers")
    }
}

private struct ChromeRow: View {
    @Environment(AppModel.self) private var model
    let browser: ChromeConnector.Browser
    let status: ChromeConnector.Status

    var body: some View {
        LabeledContent(browser.rawValue) {
            HStack {
                switch status {
                case .browserNotFound:
                    Text("Not installed").foregroundStyle(.secondary)
                case .notConnected:
                    Text("Not connected").foregroundStyle(.secondary)
                    Button("Connect") { model.connect(browser) }
                case .connected:
                    Label("Connected", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
                    Button("Disconnect") { model.disconnect(browser) }
                case let .stale(reason):
                    Label("Needs reconnecting: \(reason)", systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
                    Button("Reconnect") { model.connect(browser) }
                    Button("Disconnect") { model.disconnect(browser) }
                }
            }
        }
    }
}
