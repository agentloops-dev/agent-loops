import AgentsKitCore
import SwiftUI

/// A Work page's image that is blocked (#7139, epic #7136): its host in full, what that host
/// learns, Show image (this one, this time) and Always allow. After a press it says that it is read,
/// or why it could not be. An image that sits in words is one compact row.
struct BlockedImageView: View {
    enum State {
        case blocked(DaemonAPI.ImageBlocked, problem: String?)
        case reading(DaemonAPI.ImageBlocked)
        case failed(String, again: Bool)

        /// Nil for an image, one that loads, and one that failed as it loaded.
        @MainActor
        init?(_ phase: PageImages<PlatformImage>.Phase) {
            switch phase {
            case .blocked(let blocked, let problem): self = .blocked(blocked, problem: problem)
            case .reading(let blocked): self = .reading(blocked)
            case .failed(let why, let again): self = .failed(why, again: again)
            case .loading, .image, .none: return nil
            }
        }
    }

    let state: State
    /// The alternative text, or the file's name.
    let name: String
    let inline: Bool
    let url: URL
    let remote: RemoteImages

    var body: some View {
        Group {
            if inline { row } else { card }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Paper.well, in: RoundedRectangle(cornerRadius: Paper.Radius.card))
        .overlay(RoundedRectangle(cornerRadius: Paper.Radius.card).strokeBorder(Paper.rule, lineWidth: 1))
        .accessibilityElement(children: .contain)
    }

    // MARK: Card

    private var card: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            icon
            VStack(alignment: .leading, spacing: 3) {
                Text(verbatim: name).fontWeight(.semibold)
                switch state {
                case .blocked(let blocked, let problem):
                    words(blocked)
                    if let problem { failure(problem).padding(.top, 2) }
                    buttons {
                        Button(ImageHostsWords.show) { remote.page.show(url) }
                        allow(blocked.host)
                    }
                case .reading(let blocked):
                    words(blocked)
                    buttons {
                        ProgressView().controlSize(.small)
                        Button(ImageHostsWords.reading) {}.disabled(true)
                    }
                case .failed(let why, let again):
                    fine(ImageHostsWords.failed(why))
                    if again { buttons { tryAgain } }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .appText(.reading)
        .padding(.horizontal, 13)
        .padding(.vertical, 11)
    }

    @ViewBuilder
    private func words(_ blocked: DaemonAPI.ImageBlocked) -> some View {
        Text(Self.marked(blocked)).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
        fine(ImageHostsWords.learns(blocked.host, machine: remote.machineName))
    }

    private func buttons(@ViewBuilder _ content: () -> some View) -> some View {
        // Under each other where they do not fit, so the host's end, which says who owns it, is not cut.
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) { content() }
            VStack(alignment: .leading, spacing: 6) { content() }
        }
        .buttonStyle(.paper)
        .appText(.supporting)
        .padding(.top, 6)
    }

    private func fine(_ text: String) -> some View {
        Text(verbatim: text).appText(.supporting).foregroundStyle(Paper.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func failure(_ text: String) -> some View {
        Text(verbatim: text).appText(.supporting).foregroundStyle(StateTint.failure.style(or: .primary))
            .fixedSize(horizontal: false, vertical: true)
    }

    private var icon: some View {
        Image(icon: ImageHostsWords.symbol).foregroundStyle(Paper.tertiary).accessibilityHidden(true)
    }

    private func allow(_ host: String) -> some View {
        Button { Task { await remote.page.allow(host, from: url, machineName: remote.machineName) } } label: {
            Text(verbatim: ImageHostsWords.allow(host)).lineLimit(1).truncationMode(.middle)
        }
        .help(ImageHostsWords.allowFull(host))
        .accessibilityLabel(ImageHostsWords.allowFull(host))
    }

    private var tryAgain: some View {
        Button(ImageHostsWords.tryAgain) { remote.page.again(url) }
    }

    /// The line that names the hosts, with each of them in bold.
    private static func marked(_ blocked: DaemonAPI.ImageBlocked) -> AttributedString {
        var text = AttributedString(ImageHostsWords.blocked(blocked))
        // The refused host is the last in the line, the redirecting one the first.
        if let range = text.range(of: blocked.host, options: .backwards) { text[range].inlinePresentationIntent = .stronglyEmphasized }
        if let from = blocked.from, let range = text.range(of: from) { text[range].inlinePresentationIntent = .stronglyEmphasized }
        return text
    }

    // MARK: Row

    private var row: some View {
        HStack(spacing: 10) {
            icon
            VStack(alignment: .leading, spacing: 1) {
                Text(verbatim: name)
                switch state {
                case .blocked(let blocked, let problem):
                    fine(ImageHostsWords.blockedShort(blocked))
                    if let problem { failure(problem) }
                case .reading(let blocked): fine(ImageHostsWords.blockedShort(blocked))
                case .failed(let why, _): fine(ImageHostsWords.failed(why))
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Group {
                switch state {
                case .blocked(let blocked, _):
                    Button(ImageHostsWords.showShort) { remote.page.show(url) }
                        .accessibilityLabel(ImageHostsWords.show)
                        .help(ImageHostsWords.learns(blocked.host, machine: remote.machineName))
                    Menu {
                        Button { Task { await remote.page.allow(blocked.host, from: url, machineName: remote.machineName) } } label: {
                            Text(verbatim: ImageHostsWords.allowFull(blocked.host))
                            Text(verbatim: ImageHostsWords.menuLearns(machine: remote.machineName))
                        }
                    } label: {
                        Image(icon: "ellipsis")
                    }
                    .menuIndicator(.hidden)
                    .accessibilityLabel(ImageHostsWords.more)
                case .reading:
                    ProgressView().controlSize(.small)
                    Button(ImageHostsWords.reading) {}.disabled(true)
                case .failed(_, let again):
                    if again { tryAgain }
                }
            }
            .buttonStyle(.paper)
            .fixedSize()
            .appText(.supporting)
        }
        .appText(.reading)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
    }
}

/// Under an image that is here for one press: where it came from, and Always allow.
struct ShownOnceCaption: View {
    let host: String
    let url: URL
    let remote: RemoteImages

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) {
                    Text(verbatim: "\(ImageHostsWords.shownOnce(host)) ·").foregroundStyle(Paper.secondary)
                    allow
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: ImageHostsWords.shownOnce(host)).foregroundStyle(Paper.secondary)
                    allow
                }
            }
            if let problem = remote.page.problem(url) {
                Text(verbatim: problem).foregroundStyle(StateTint.failure.style(or: .primary))
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .appText(.supporting)
    }

    private var allow: some View {
        Button { Task { await remote.page.allow(host, from: url, machineName: remote.machineName) } } label: {
            Text(verbatim: ImageHostsWords.allow(host)).lineLimit(1).truncationMode(.middle)
        }
        .buttonStyle(.plain)
        .foregroundStyle(Paper.accent)
        .help(ImageHostsWords.allowFull(host))
        .accessibilityLabel(ImageHostsWords.allowFull(host))
        #if os(macOS)
        .pointerStyle(.link)
        #endif
    }
}
