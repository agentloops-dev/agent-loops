import AgentsKitCore
import SwiftUI

/// Priority ▸ High, Medium, Low, From issue (or None) in a session's menu (#5849), with a check on
/// the one it goes by: the priority set on it, else From issue. The Mac's row and Session menus
/// and the Remote's long-press and ··· menus draw it; the web page's is `SessionMenu.tsx`.
struct SessionPriorityMenu: View {
    /// The item with the check.
    let checked: SessionPriorityWords.Choice
    /// "From issue", "From issue (High)" or "None".
    let followTitle: String
    /// Sends `agents/setPriority`: a level, or nil to follow the issue again.
    let set: (SessionPriority?) -> Void

    init(checked: SessionPriorityWords.Choice, followTitle: String, set: @escaping (SessionPriority?) -> Void) {
        self.checked = checked
        self.followTitle = followTitle
        self.set = set
    }

    init(agent: Agent, set: @escaping (SessionPriority?) -> Void) {
        self.init(checked: SessionPriorityWords.checked(agent),
                  followTitle: SessionPriorityWords.title(.follow, for: agent), set: set)
    }

    var body: some View {
        Menu(SessionPriorityWords.menu) {
            Picker(SessionPriorityWords.menu, selection: Binding(
                get: { checked },
                set: { choice in
                    guard choice != checked else { return }
                    set(choice.priority)
                })) {
                ForEach(SessionPriorityWords.Choice.allCases) { choice in
                    Text(choice == .follow ? followTitle : SessionPriorityWords.title(choice))
                        .tag(choice)
                }
            }
            .pickerStyle(.inline)
            .labelsHidden()
        }
    }
}
