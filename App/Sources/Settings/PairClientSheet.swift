import AgentsKitCore
import SwiftUI

/// Settings ▸ Clients ▸ Pair a Client… (#7270): one sheet for each kind of client, each a one-use
/// code from `clients/startPairing`. An iPhone or iPad scans a QR code (058, US5); a Mac pastes
/// the code as text, as a browser does, but a browser's code is its own kind and says where to
/// paste it (071, frame E; #1459).
///
/// The sheet asks for a fresh code when it opens and each time the kind changes, and lets it go
/// when it closes, so a code works only while somebody looks at it. It names the client that
/// arrives since the code was made.
struct PairClientSheet: View {
    enum Kind: CaseIterable, Identifiable {
        case device, mac, browser
        var id: Self { self }

        var label: String {
            switch self {
            case .device: "iPhone or iPad"
            case .mac: "Mac"
            case .browser: "Browser"
            }
        }
    }

    @Environment(\.dismiss) private var dismiss
    let control: ControlSettingsModel
    @State private var kind: Kind
    @State private var shown: DaemonAPI.ControlCodeShown?
    @State private var problem: String?
    /// Who was there when the code was made, so whoever it lets in can be named.
    @State private var before: Set<UUID> = []

    init(control: ControlSettingsModel, kind: Kind = .device) {
        self.control = control
        _kind = State(initialValue: kind)
    }

    /// The client that arrived since the code was made: the code let it in, and is spent.
    private var paired: ClientRecord? {
        guard shown != nil else { return nil }
        return control.clients.first { !before.contains($0.id) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Pair a Client").appText(.title)
            Picker("Client", selection: $kind) {
                ForEach(Kind.allCases) { Text($0.label).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            if let paired {
                Label(kind == .device ? "\(paired.name) is paired" : "\(paired.name) joined with this code. It can’t be used again.",
                      icon: "checkmark.circle")
                    .appText(.reading)
                    .fixedSize(horizontal: false, vertical: true)
                if kind == .device {
                    Text("It reaches your agents through the control plane, as a Device.")
                        .foregroundStyle(.secondary)
                }
            } else if let shown {
                if kind == .device { scan(shown) } else { text(shown) }
            } else if let problem {
                // The pairing's note (#4737), in place of its code.
                SheetNote(text: problem, reservesSpace: false)
            } else {
                ProgressView().controlSize(.small).frame(maxWidth: .infinity)
            }
            HStack {
                Spacer()
                Button(paired == nil && kind == .device ? "Cancel" : "Done") { dismiss() }
                    .keyboardShortcut(paired == nil && kind == .device ? .cancelAction : .defaultAction)
            }
        }
        .padding(24)
        .frame(width: 480)
        // A change of kind makes a new code: the old one stops first, so it can't stop the new one.
        .task(id: kind) {
            shown = nil
            problem = nil
            await control.stopCodes()
            guard !Task.isCancelled else { return }
            before = Set(control.clients.map(\.id))
            let code = await control.startCode(forHost: false, browser: kind == .browser)
            guard !Task.isCancelled else { return }
            shown = code
            if code == nil { problem = control.problem ?? "Couldn't make a pairing code." }
        }
        .onDisappear { Task { await control.stopCodes() } }
    }

    /// An iPhone or iPad: a QR code to scan with Agent Loops.
    private func scan(_ shown: DaemonAPI.ControlCodeShown) -> some View {
        VStack(spacing: 16) {
            Text("Open Agent Loops on your iPhone or iPad and scan this code.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                // Wrapped, not cut short: this is the instruction.
                .fixedSize(horizontal: false, vertical: true)
            QRCode(text: shown.text)
                .frame(width: 220, height: 220)
                .accessibilityLabel("Pairing code")
                .accessibilityValue(shown.text)
            TimelineView(.periodic(from: .now, by: 1)) { context in
                Text(remaining(until: shown.expires, now: context.date))
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
        }
        .frame(maxWidth: .infinity)
    }

    /// A Mac or a browser: the code as text, since a Mac has no camera.
    private func text(_ shown: DaemonAPI.ControlCodeShown) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(shown.text)
                .appText(.code)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Paper.raised, in: RoundedRectangle(cornerRadius: 8))
                .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(Paper.rule, lineWidth: 1))
                // One element of its own: a label on the selectable Text overflows the stack (#1724).
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("Code")
                .accessibilityValue(shown.text)
            HStack(alignment: .firstTextBaseline) {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Text(instructions(until: shown.expires, now: context.date))
                        .appText(.supporting).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer()
                Button("Copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(shown.text, forType: .string)
                }
                .buttonStyle(.paper)
            }
        }
    }

    private func remaining(until expires: Date, now: Date) -> String {
        let seconds = max(0, Int(expires.timeIntervalSince(now)))
        if seconds == 0 { return "This code has run out. Close this and pair again." }
        return String(format: "Good for %d:%02d", seconds / 60, seconds % 60)
    }

    private func instructions(until expires: Date, now: Date) -> String {
        let left = max(0, Int(expires.timeIntervalSince(now)))
        let time = left == 0 ? "It has run out; close this and ask again." : "It works once, for \(left / 60):\(String(format: "%02d", left % 60))."
        if kind == .browser { return "\(browserWhere) That browser can then do all this window can. \(time)" }
        return "On the other Mac, open Agent Loops, choose Connect to a control plane and paste this. That Mac can then do all this window can. \(time)"
    }

    /// Where to paste a browser's code: the page on the control plane's own domain, for a
    /// browser anywhere (#1459), and the page on this Mac while it is served, else why it
    /// isn't, as the Overview's row says (071 R3).
    private var browserWhere: String {
        let anywhere = control.status?.publicWeb
        guard let web = control.status?.web else {
            if let anywhere { return "In any browser, open \(anywhere) and paste it in." }
            return "Turn on Serve Agent Loops to browsers on this Mac in Settings ▸ Control plane first."
        }
        let here = web.served ? "On this Mac, open \(web.address) and paste it in."
            : "\(web.summary) Press Try Again beside Serve Agent Loops to browsers on this Mac in Settings ▸ Control plane, then open \(web.address) on this Mac and paste it in."
        guard let anywhere else { return here }
        return "In any browser, open \(anywhere) and paste it in. \(here)"
    }
}
