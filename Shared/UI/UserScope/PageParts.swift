import SwiftUI

/// The parts the Secrets, Accounts and Environment pages share (#5257, #5258, #5260): a filter chip with its count, a
/// label of the detail's grid, and the detail's Needs you row.

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
