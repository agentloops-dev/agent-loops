import AgentsKitCore
import SwiftUI
import UIKit

/// A Work page in a scene of its own on an iPad (#5475), beside the main one, whose place is not
/// touched by it. A session opens in a scene of its own (#888); Work's lists and a new session open in
/// the main scene.
struct RemoteWorkScene: View {
    @Environment(RemoteModel.self) private var model
    @Environment(\.openWindow) private var openWindow
    @Environment(\.scenePhase) private var scenePhase
    /// The page, as the scene was opened with it and is restored with it. A drag out of a list opens
    /// the scene first and names the page after.
    @Binding var key: WorkWindowKey?
    /// This scene, among the app's, for where the person is (021).
    @State private var sceneID = UUID()

    var body: some View {
        NavigationStack {
            WorkWindowPage(base: model, key: $key, showMain: { model.showMainScene() },
                           showSession: { openWindow(id: PoppedChat.sceneID, value: $0) })
                .paperGround()
        }
        .background(Paper.ground)
        .onContinueUserActivity(OpenWorkWindow.activityType) { activity in
            if let dragged = OpenWorkWindow.key(in: activity) { key = dragged }
        }
        // Restored on its own, with no main scene to start the link.
        .task { await model.connect() }
        .onChange(of: scenePhase, initial: true) { _, phase in model.scenePhase(phase, of: sceneID) }
    }
}

extension RemoteModel {
    /// The main scene in front; a new one when the person closed it.
    func showMainScene() {
        let request = mainScene.map { UISceneSessionActivationRequest(session: $0) } ?? UISceneSessionActivationRequest()
        UIApplication.shared.activateSceneSession(for: request)
    }
}

/// Says which scene this view is in.
struct SceneSessionReader: UIViewRepresentable {
    let found: (UISceneSession?) -> Void

    func makeUIView(context: Context) -> Reader {
        let view = Reader()
        view.found = found
        return view
    }

    func updateUIView(_ view: Reader, context: Context) { view.found = found }

    final class Reader: UIView {
        var found: (UISceneSession?) -> Void = { _ in }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if let session = window?.windowScene?.session { found(session) }
        }
    }
}
