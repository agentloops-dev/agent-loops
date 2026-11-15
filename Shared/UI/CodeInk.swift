import CodeText
import SwiftUI

/// What code is printed in (041 research R4).
///
/// Nine roles, one colour each in light and dark, taken from the numbers in
/// `Packages/CodeText/Sources/CodeText/InkValues.swift`, where they are checked for contrast
/// against `Paper.ground` and `Paper.well` and for anything that reads as red. Red stays
/// `StateTint`'s, for something having gone wrong; code is never coloured to look like an
/// error. A diff's added and removed lines are the one exception (#5474): green and red from
/// `design/tokens.json`'s `color.diff`, always with their + and − signs, so colour is never
/// the only mark.
///
/// Quiet on purpose: code sits in a well on paper, and the colours are inks rather than a
/// screen theme. Keywords carry weight as well as colour, and comments are set in italic, so
/// the two things read most often are told apart in grey-scale too.
enum CodeInk {
    /// The attributes a stretch of code takes, laid over the line's `TextStep.code` font, or
    /// over the font given: a zoomed page's (#1419).
    static func attributes(for role: CodeRole, font: Font = TextStep.code.font) -> AttributeContainer {
        var container = AttributeContainer()
        guard let color = colors[role] else { return container }
        container.foregroundColor = color
        switch role {
        case .keyword: container.font = font.weight(.semibold)
        case .comment: container.font = font.italic()
        default: break
        }
        return container
    }

    /// A diff line's colours (#5474, `Paper.diff…`): the wash under the line, the bar at its
    /// leading edge, and the stronger mark under the words that changed. Nil for context.
    struct DiffTint {
        let wash: Color
        let bar: Color
        let word: Color
    }

    static func diff(_ kind: DiffRow.Kind) -> DiffTint? {
        switch kind {
        case .added: DiffTint(wash: Paper.diffAddedWash, bar: Paper.diffAddedBar, word: Paper.diffAddedWord)
        case .removed: DiffTint(wash: Paper.diffRemovedWash, bar: Paper.diffRemovedBar, word: Paper.diffRemovedWord)
        case .context: nil
        }
    }

    /// The width of a diff line's bar.
    static let diffBar: CGFloat = 3

    private static let colors: [CodeRole: Color] = Dictionary(uniqueKeysWithValues:
        CodeRole.allCases.compactMap { role in
            guard let light = InkValues.light[role], let dark = InkValues.dark[role] else {
                return nil
            }
            return (role, Color(light: light, dark: dark))
        })
}
