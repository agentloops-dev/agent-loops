import AgentsKitCore
import SwiftUI

/// The Cost report (#6884): what the work used, in three tabs, Spend, Tokens and Time.
///
/// A page in the detail column, picked from Reports in the sidebar. Each tab is a place of its
/// own in the window's history (`SidebarItem.spending`, `.tokens` and `.time`), so Back goes to
/// the tab before.
struct CostView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(spacing: 0) {
            Picker("Show", selection: tab) {
                ForEach(CostTab.allCases) { Text($0.title).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding([.horizontal, .top], 24)
            switch tab.wrappedValue {
            case .spend: SpendingView()
            case .tokens: TokensView()
            case .time: TimeView()
            }
        }
        .navigationTitle("Cost")
    }

    private var tab: Binding<CostTab> {
        Binding(get: { model.costTab ?? .spend }, set: { model.sidebarItem = $0.item })
    }
}

/// The Cost report's tabs (#6884).
enum CostTab: CaseIterable, Identifiable {
    case spend, tokens, time

    var id: Self { self }

    var title: String {
        switch self {
        case .spend: "Spend"
        case .tokens: "Tokens"
        case .time: "Time"
        }
    }

    var item: SidebarItem {
        switch self {
        case .spend: .spending
        case .tokens: .tokens
        case .time: .time
        }
    }
}

extension AppModel {
    /// The Cost report's tab on show, and nil with another page open (#6884).
    var costTab: CostTab? {
        if showsSpending { return .spend }
        if showsTokens { return .tokens }
        return showsTime ? .time : nil
    }
}
