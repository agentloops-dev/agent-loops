---
name: task-tuple-returns-miscompile
description: Swift 6.4.2 -O drops a task child's tuple key when do and catch both return a tuple; the window crashed in completeFuture / TaskGroup::offer (#5946)
metadata:
  type: project
---

In an optimised build (Xcode 27.2, Swift 6.4.2), an async closure that returns a tuple from both a `do` that awaits and its `catch`, such as `do { return (machine.id, .success(try await …)) } catch { return (machine.id, .failure(error)) }`, loses the store of the first element on the success path (swiftlang/swift#92698, closed upstream but still in 6.4.2). As a task-group child with a `String` key (`MachineRef`), it damages the group. A 6-line repro at `-O` aborted with `__cxa_pure_virtual` in `swift::AsyncTask::completeFuture`, frame for frame the window's crash of 2026-11-16 (#5946). Its `for await` got no element. Debug builds and every test are correct.

**Why:** a runtime crash with no frame of ours and nothing in the logs took a whole lane to place; the cause is a compiler bug, not our concurrency.

**How to apply:** in a task child or `Task`, build the result with `Result.catching { try await … }` (AgentsKitCore), then return the tuple from one place. `scripts/check-task-tuple-returns.py` (repo-lint) fails on the bad shape. For the next crash in `libswift_Concurrency` with no frame of ours, first compile the suspect shape alone with `swiftc -O -parse-as-library` and run it about 30 times: it crashed 1 run in 30 here, and seldom under lldb.
