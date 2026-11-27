import AgentsKitCore
import SwiftUI
#if os(macOS)
import AppKit
#endif

/// The parts the Secrets, Accounts and Environment pages share (#5257, #5258, #5260, #5706): a filter chip with its
/// count, a label of the detail's grid, the detail's Needs you row, the search, the list beside the detail, and the
/// detail's problem line and frame.

/// A filter chip with its count; `warns` puts the warning mark first (Needs you).
struct PageChip: View {
    let title: String
    let count: Int
    let warns: Bool
    let on: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                if warns { Image(icon: "exclamationmark.triangle") }
                Text("\(title) \(count)")
            }
            .appText(.fine)
            .foregroundStyle(on ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary))
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(on ? AnyShapeStyle(.selection) : AnyShapeStyle(.clear), in: Capsule())
            .paperRaised(in: Capsule())
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(title), \(count)")
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}

/// A label in the first column of a detail's grid.
struct PageGridLabel: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text).foregroundStyle(.secondary).gridColumnAlignment(.leading)
    }
}

/// The detail's Needs you row, when the item needs the person.
struct PageNeedsYouRow: View {
    let warning: String?

    var body: some View {
        if let warning {
            GridRow {
                PageGridLabel("Needs you")
                Label(warning, icon: "exclamationmark.triangle").tinted(.warning)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

/// The page's search field and its hidden ⌘F button (#5706). In an `HStack` the two are two children, as before.
struct PageSearchField: View {
    let prompt: String
    let label: String
    let clearLabel: String
    @Binding var text: String
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 6) {
            Image(icon: "magnifyingglass").foregroundStyle(.secondary)
            TextField(prompt, text: $text)
                .textFieldStyle(.plain)
                .focused($focused)
                .autocorrectionDisabled()
                #if os(iOS)
                .textInputAutocapitalization(.never)
                #endif
                .accessibilityLabel(label)
                #if os(macOS)
                .onExitCommand { text = "" }
                #endif
            if !text.isEmpty {
                Button { text = "" } label: { Image(icon: "xmark.circle.fill") }
                    .buttonStyle(.borderless)
                    .accessibilityLabel(clearLabel)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 5)
        .frame(minWidth: 160)
        .paperWell(in: RoundedRectangle(cornerRadius: Paper.Radius.control))
        // ⌘F puts the cursor in the search (wireframes §2).
        Button("") { focused = true }
            .keyboardShortcut("f", modifiers: .command)
            .hidden()
            .frame(width: 0, height: 0)
            .accessibilityHidden(true)
    }
}

/// The page under its header (#5706): on a wide page the list on the left and the detail beside it, on a narrow one
/// `narrow`.
struct PageListAndDetail<ListPart: View, DetailPart: View, NarrowPart: View>: View {
    let wide: Bool
    let width: CGFloat
    @ViewBuilder let list: ListPart
    @ViewBuilder let detail: DetailPart
    @ViewBuilder let narrow: NarrowPart

    var body: some View {
        if wide {
            HStack(spacing: 0) {
                list
                    .frame(width: min(440, width * 0.45))
                Divider()
                detail
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
        } else {
            narrow
        }
    }
}

/// The detail's problem line, under the facts, when a save failed (#5706).
struct PageProblemLine: View {
    let problem: String?

    var body: some View {
        if let problem {
            Text(problem).appText(.fine).tinted(.failure).fixedSize(horizontal: false, vertical: true)
        }
    }
}

extension View {
    /// Hides a shown secret value when the secrets change, the page goes, or the app goes to the back (#5706).
    func hidesShownSecret(_ show: SecretShowState?, secrets: DaemonAPI.UserSecretNames?) -> some View {
        self
            .onChange(of: secrets) { _, names in show?.secretsChanged(names?.secrets ?? []) }
            .onDisappear { show?.hide() }
            #if os(macOS)
            .onReceive(NotificationCenter.default.publisher(for: NSApplication.didResignActiveNotification)) { _ in show?.hide() }
            #endif
    }

    /// The detail's width, and `reset` when it opens and when editing starts or ends (#5706).
    func pageDetail(isEditing: Bool, reset: @escaping () -> Void) -> some View {
        self
            .frame(maxWidth: 560, alignment: .leading)
            .onAppear { reset() }
            .onChange(of: isEditing) { _, _ in reset() }
    }
}
