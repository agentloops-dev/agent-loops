import AgentsKitCore
import SwiftUI

/// A row's Reveal (an eye; a spinner while its copy is fetched) and Copy (a check for 2 s once copied), #5115.
struct SecretShowButtons: View {
    let row: DaemonAPI.UserSecretRow
    let show: SecretShowState
    let control: DaemonClient?
    /// The account it is opened from, for the audit entry (#5116, #5258).
    var account: DaemonAPI.UserSealedSecretRequest.Account?

    var body: some View {
        let shown = show.shown?.name == row.name
        let off = control == nil || show.noCopy.contains(row.name)
        HStack(spacing: 4) {
            if show.fetching == row.name {
                ProgressView().controlSize(.small).frame(width: 16, height: 16)
            } else {
                Button { Task { await show.toggle(row, account: account, on: control) } } label: {
                    Image(icon: shown ? "eye.slash" : "eye")
                }
                .buttonStyle(.borderless)
                .help(shown ? UserScopeWords.hide : UserScopeWords.reveal)
                .accessibilityLabel("\(shown ? UserScopeWords.hide : UserScopeWords.reveal) \(row.name)")
                .disabled(off)
            }
            Button { Task { await show.copy(row, account: account, on: control) } } label: {
                Image(icon: show.copied?.name == row.name && show.copied?.part == nil ? "checkmark" : "doc.on.doc")
            }
            .buttonStyle(.borderless)
            .help(UserScopeWords.copy)
            .accessibilityLabel("\(UserScopeWords.copy) \(row.name)")
            .disabled(off)
        }
    }
}

/// The value shown, in a well, in the code font, that a person cannot select: Copy is the only
/// way to copy it, so each copy is in the audit log. A card shows each part with its own Copy.
struct SecretShownValue: View {
    let row: DaemonAPI.UserSecretRow
    let shown: SecretShowState.Shown
    let show: SecretShowState
    let control: DaemonClient?
    var account: DaemonAPI.UserSealedSecretRequest.Account?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let card = shown.card {
                ForEach(UserScopeWords.cardParts, id: \.self) { part in
                    HStack {
                        Text(UserScopeWords.partLabel(part)).appText(.fine).foregroundStyle(.secondary)
                            .frame(width: 96, alignment: .leading)
                        Text(part == .number ? card.spacedNumber : card.part(part)).appText(.code)
                        Spacer()
                        Button { Task { await show.copy(row, part: part, account: account, on: control) } } label: {
                            Image(icon: show.copied == .init(name: row.name, part: part) ? "checkmark" : "doc.on.doc")
                        }
                        .buttonStyle(.borderless)
                        .help(UserScopeWords.copy)
                        .accessibilityLabel("\(UserScopeWords.copy) \(UserScopeWords.partLabel(part)) of \(row.name)")
                    }
                }
            } else {
                Text(shown.value).appText(.code)
                    .lineLimit(nil)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityLabel("\(row.name) value")
                    .accessibilityValue(shown.value)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .paperWell(in: RoundedRectangle(cornerRadius: Paper.Radius.control))
    }
}

/// Under a row: how long the value still shows, that it was copied, or why it cannot.
struct SecretShowLines: View {
    let row: DaemonAPI.UserSecretRow
    let show: SecretShowState

    var body: some View {
        if let shown = show.shown, shown.name == row.name {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                Text(UserScopeWords.hidesIn(Int(shown.until.timeIntervalSince(context.date).rounded(.up)), card: shown.card != nil))
                    .appText(.fine).foregroundStyle(.secondary)
            }
        }
        if let copied = show.copied, copied.name == row.name {
            Label(UserScopeWords.copied(copied.part), icon: "checkmark")
                .appText(.fine).tinted(.vouched)
        }
        if show.noCopy.contains(row.name) {
            Text(UserScopeWords.noCopyYet).appText(.fine).tinted(.attention)
                .fixedSize(horizontal: false, vertical: true)
        }
        if let problem = show.problems[row.name] {
            Text(problem).appText(.fine).tinted(.failure)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

/// A secret's line (#4159): "On 3 machines · Added 27 d ago · Used 2 h ago". On the Mac a hover
/// on a part gives its date and time; on iOS a long press on the line gives them all.
struct SecretInfoLine: View {
    let parts: [SecretInfoWords.Part]

    var body: some View {
        #if os(macOS)
        HStack(spacing: 0) {
            ForEach(Array(parts.enumerated()), id: \.offset) { index, part in
                if index > 0 { Text(" · ") }
                Text(part.text).help(part.help ?? "")
            }
        }
        .appText(.fine).foregroundStyle(.secondary)
        #else
        Text(SecretInfoWords.joined(parts)).appText(.fine).foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .contextMenu {
                ForEach(parts.compactMap(\.help), id: \.self) { help in
                    Text(help)
                }
            }
        #endif
    }
}

/// Add a Secret… and Replace…: a name and a value. The value is never shown again.
struct SecretSheet: View {
    /// The secret replaced, or nil for a new one.
    let name: String?
    /// Save, with the name, the value, Ask before use and the description (#4167).
    let save: (String, String, Bool, String?) async -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var typedName = ""
    @State private var value = ""
    @State private var askBeforeUse: Bool
    @State private var about: String
    @State private var problem: String?
    @State private var saving = false

    /// `asks`: Ask before use as the toggle starts: the replaced secret's, on for a new one (#4162).
    /// `description`: the replaced secret's, to change (#4167).
    /// `suggested`: a new secret's name, filled in (Save as a Secret Instead… on the Environment page, #5260).
    init(name: String?, asks: Bool, description: String?, suggested: String = "",
         save: @escaping (String, String, Bool, String?) async -> String?) {
        self.name = name
        self.save = save
        _typedName = State(initialValue: suggested)
        _askBeforeUse = State(initialValue: asks)
        _about = State(initialValue: description ?? "")
    }

    private var chosen: String { name ?? typedName.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        Form {
            Section {
                if name == nil {
                    TextField("Name", text: $typedName, prompt: Text("GITHUB_TOKEN"))
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.characters)
                        #endif
                }
                SecureField("Value", text: $value)
                SecretDescriptionField(text: $about)
                AskBeforeUseToggle(isOn: $askBeforeUse)
            } header: {
                Text(name.map { "Replace \($0)" } ?? "Add a Secret")
            } footer: {
                Text(problem ?? UserScopeWords.nameHint).appText(.fine)
                    .tinted(problem == nil ? .none : .failure)
            }
            HStack {
                Button("Cancel", role: .cancel) { dismiss() }
                Spacer()
                Button(name == nil ? "Add" : "Replace") {
                    Task {
                        saving = true
                        problem = await save(chosen, value, askBeforeUse, UserScopeWords.description(about))
                        saving = false
                        if problem == nil { dismiss() }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving || value.isEmpty || !UserScopeWords.isSecretName(chosen))
            }
            .rowButtons()
        }
        .paperForm()
        #if os(macOS)
        .frame(minWidth: 360)
        #endif
        .onDisappear { value = "" }
    }
}

/// Add a Card… and a card's Replace… (#3531): a payment card for agents to fill into a page,
/// only one with a spend limit (a merchant or category lock is recommended). Its parts are one secret; none is shown again.
struct CardSheet: View {
    /// The card replaced, or nil for a new one.
    let name: String?
    /// Save, with the name, the card's parts as one value, Ask before use and the description (#4167).
    let save: (String, String, Bool, String?) async -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var typedName = ""
    @State private var number = ""
    @State private var expiry = ""
    @State private var cvc = ""
    @State private var holder = ""
    @State private var zip = ""
    @State private var line1 = ""
    @State private var line2 = ""
    @State private var city = ""
    @State private var region = ""
    @State private var country = ""
    @State private var locked = false
    @State private var askBeforeUse: Bool
    @State private var about: String
    @State private var problem: String?
    @State private var saving = false

    /// `asks`: Ask before use as the toggle starts: the replaced card's, on for a new one (#4162).
    /// `description`: the replaced card's, to change (#4167).
    init(name: String?, asks: Bool, description: String?, save: @escaping (String, String, Bool, String?) async -> String?) {
        self.name = name
        self.save = save
        _askBeforeUse = State(initialValue: asks)
        _about = State(initialValue: description ?? "")
    }

    private var chosen: String { name ?? typedName.trimmingCharacters(in: .whitespaces) }
    private var card: CardSecret {
        CardSecret(number: number, expiry: expiry, cvc: cvc, name: holder, zip: zip,
                   line1: line1, line2: line2, city: city, state: region, country: country)
    }

    var body: some View {
        Form {
            Section {
                Text(UserScopeWords.cardRule).appText(.supporting)
                    .fixedSize(horizontal: false, vertical: true)
                Toggle(UserScopeWords.cardConfirm, isOn: $locked)
                    .accessibilityLabel(UserScopeWords.cardConfirm)
                    #if os(macOS)
                    .toggleStyle(.checkbox)
                    #endif
            }
            Section {
                if name == nil {
                    TextField("Name", text: $typedName, prompt: Text("SHOP_CARD"))
                        .autocorrectionDisabled()
                }
                SecureField("Card number", text: $number, prompt: Text("Digits"))
                TextField("Expiry", text: $expiry, prompt: Text("MM/YY"))
                    .autocorrectionDisabled()
                SecureField("CVC", text: $cvc, prompt: Text("3 or 4 digits"))
                TextField("Name on card", text: $holder, prompt: Text("As on the card"))
                    .autocorrectionDisabled()
                TextField("Billing ZIP", text: $zip, prompt: Text("ZIP or postal code"))
                    .autocorrectionDisabled()
            } header: {
                Text(name.map { "Replace \($0)" } ?? "Add a Card")
            }
            // The billing address (#5569): optional, for a sign-up that bills. fill_secret fills
            // each line into its own field.
            Section {
                TextField("Address line 1", text: $line1, prompt: Text("Street"))
                    .autocorrectionDisabled()
                TextField("Address line 2", text: $line2, prompt: Text("Optional"))
                    .autocorrectionDisabled()
                TextField("City", text: $city, prompt: Text("City or town"))
                    .autocorrectionDisabled()
                TextField("State", text: $region, prompt: Text("State, province or region"))
                    .autocorrectionDisabled()
                TextField("Country", text: $country, prompt: Text("Country"))
                    .autocorrectionDisabled()
            } header: {
                Text(UserScopeWords.cardAddressHeader)
            } footer: {
                Text(UserScopeWords.cardAddressHint).appText(.fine)
            }
            Section {
                SecretDescriptionField(text: $about)
                AskBeforeUseToggle(isOn: $askBeforeUse)
            } footer: {
                Text(problem ?? UserScopeWords.nameHint).appText(.fine)
                    .tinted(problem == nil ? .none : .failure)
            }
            HStack {
                Button("Cancel", role: .cancel) { dismiss() }
                Spacer()
                Button(name == nil ? "Add" : "Replace") {
                    if let wrong = card.problem {
                        problem = wrong
                        return
                    }
                    Task {
                        saving = true
                        problem = await save(chosen, card.value, askBeforeUse, UserScopeWords.description(about))
                        saving = false
                        if problem == nil { dismiss() }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving || !locked || number.isEmpty || cvc.isEmpty || !UserScopeWords.isSecretName(chosen))
            }
            .rowButtons()
        }
        .paperForm()
        #if os(macOS)
        .frame(minWidth: 400)
        #endif
        .onDisappear { number = ""; cvc = ""; line1 = ""; line2 = "" }
    }
}

/// A secret's Description field (#4167): one or two sentences, so it wraps rather than scrolls,
/// with the words that it is not sealed. Add a Secret…, Add a Card…, Replace… and Add for a
/// Session… draw it.
struct SecretDescriptionField: View {
    @Binding var text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            TextField(UserScopeWords.descriptionLabel, text: $text, prompt: Text(UserScopeWords.descriptionPrompt), axis: .vertical)
                .lineLimit(1...3)
                #if os(iOS)
                .textInputAutocapitalization(.sentences)
                #endif
            Text(UserScopeWords.descriptionHint).appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
