import Foundation

extension Result where Failure == any Error {
    /// What `body` gave, or the error it threw, as one value (#5946).
    ///
    /// A task-group child or a `Task` must build its result first, then return its tuple
    /// from one place: `let got = await Result.catching { try await … }; return (key, got)`.
    /// Never return the tuple from both a `do` that awaits and its `catch`.
    /// In an optimised build, Swift 6.4.2's LLVM drops the store of the tuple's first
    /// element on the success path (swiftlang/swift#92698). With a `String` key such as
    /// `MachineRef`, the child's result is never written, so the group reads memory nobody
    /// wrote, and the window crashes in the concurrency runtime
    /// (`swift::AsyncTask::completeFuture`, `swift::TaskGroup::offer`). The same happens for
    /// any tuple returned from both the `do` and the `catch`. `scripts/check-task-tuple-returns.py`
    /// fails on that shape.
    public static func catching(
        isolation: isolated (any Actor)? = #isolation,
        _ body: () async throws -> Success
    ) async -> Self {
        do {
            return .success(try await body())
        } catch {
            return .failure(error)
        }
    }

    /// The error, or nil on success.
    public var error: (any Error)? {
        if case let .failure(error) = self { return error }
        return nil
    }
}
