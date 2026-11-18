#!/usr/bin/env python3
"""No tuple returned from both a `do` that awaits and its `catch` (#5946).

In an optimised build, Swift 6.4.2's LLVM drops the store of a tuple's first element on
the success path of an async closure shaped like this (swiftlang/swift#92698):

    group.addTask {
        do { return (machine.id, .success(try await read(machine))) }
        catch { return (machine.id, .failure(error)) }
    }

The element is stored before the `await`, and stored again only on the `catch` path, so
dead store elimination drops the first store. With a `String` key such as `MachineRef`, the
child's result is memory nobody wrote: the window crashed in the concurrency runtime
(`swift::AsyncTask::completeFuture` calling a pure virtual function, and
`swift::TaskGroup::offer` reading 0x20), with no frame of ours. A debug build and every
test are correct, so only this check sees it.

Build the result first and return the tuple from one place:

    let got = await Result.catching { try await read(machine) }
    return (machine.id, got)

This reads the Swift sources of the apps and packages. It fails on a `catch` whose block
starts with `return (` when its `do` block awaits and also returns a tuple. A line that is
safe for a reason this cannot see says so with `// task-tuple: <why>` on the `catch` line.
"""
import pathlib
import re
import sys

root = pathlib.Path(__file__).resolve().parent.parent

FOLDERS = ("App/Sources", "Shared", "Remote", "RemoteNotify", "RemoteShare", "RemoteWidget", "Host")
CATCH = re.compile(r"\}\s*catch\b[^{\n]*\{\s*return\s*\(")
DO = re.compile(r"\bdo\s*$")
TUPLE_RETURN = re.compile(r"\breturn\s*\(")


def opening(text, close):
    """The index of the `{` that the `}` at `close` ends, or -1."""
    depth = 0
    for at in range(close, -1, -1):
        if text[at] == "}":
            depth += 1
        elif text[at] == "{":
            depth -= 1
            if depth == 0:
                return at
    return -1


def problems_in(text):
    found = []
    for match in CATCH.finditer(text):
        line_start = text.rfind("\n", 0, match.start()) + 1
        line_end = text.find("\n", match.start())
        line = text[line_start:line_end if line_end != -1 else len(text)]
        if "task-tuple:" in line:
            continue
        start = opening(text, match.start())
        if start == -1 or not DO.search(text[:start].rstrip()):
            continue
        body = text[start:match.start()]
        if "await" in body and TUPLE_RETURN.search(body):
            found.append(text.count("\n", 0, match.start()) + 1)
    return found


SELFTEST = [
    # The #5946 shape: the key stored before the await, again only in the catch.
    ("""group.addTask {
    do {
        return (machine.id, .success(try await read(machine)))
    } catch {
        return (machine.id, .failure(error))
    }
}
""", [4]),
    # On one line.
    ("""group.addTask { do { return (k, try await f(k), nil) } catch { return (k, nil, "\\(error)") } }
""", [1]),
    # A nested closure in the do block, with a tuple after a let.
    ("""do {
    let t = try await f { $0 }
    if t.isEmpty { log() }
    return (i, t, nil)
} catch {
    return (i, nil, error.localizedDescription)
}
""", [5]),
    # The fix: the result first, one return.
    ("""group.addTask {
    let got = await Result.catching { try await read(machine) }
    return (machine.id, got)
}
""", []),
    # A catch that returns no tuple.
    ("""do {
    return (k, try await f(k))
} catch {
    return nil
}
""", []),
    # No await in the do: not the shape.
    ("""do {
    return (k, try f(k))
} catch {
    return (k, nil)
}
""", []),
    # Said safe, with why.
    ("""do {
    return (k, try await f(k))
} catch { // task-tuple: the key is an Int and this is not a task
    return (k, nil)
}
""", []),
]


def selftest():
    failed = 0
    for text, want in SELFTEST:
        got = problems_in(text)
        if got != want:
            failed += 1
            print(f"check-task-tuple-returns selftest: want {want}, got {got} for:\n{text}", file=sys.stderr)
    return 1 if failed else 0


def sources():
    for folder in FOLDERS:
        yield from sorted((root / folder).rglob("*.swift"))
    for package in sorted((root / "Packages").glob("*/Sources")):
        yield from sorted(package.rglob("*.swift"))


def main():
    if "--selftest" in sys.argv[1:]:
        return selftest()
    problems = []
    for path in sources():
        if "/.build/" in str(path):
            continue
        text = path.read_text()
        lines = text.splitlines()
        relative = str(path.relative_to(root))
        for number in problems_in(text):
            problems.append(f"{relative}:{number}: {lines[number - 1].strip()}")
    for problem in problems:
        print(problem)
    if problems:
        print(f"\n{len(problems)} tuple(s) returned from both a do that awaits and its catch: an optimised build "
              "drops the tuple's first element (swiftlang/swift#92698), and the window crashed in the "
              "concurrency runtime (#5946). Build the result first with Result.catching, then return the "
              "tuple from one place.", file=sys.stderr)
        return 1
    print("No tuple is returned from both a do that awaits and its catch.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
