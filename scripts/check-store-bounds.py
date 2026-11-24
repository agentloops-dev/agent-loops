#!/usr/bin/env python3
"""A data store with no kind or no bounds fails (#6683, specs/data-bounds/store-kinds.md).

It finds each place where product code opens a store (OPENINGS, below):

- Go: a file written by path, a folder store, a rolling log, or a control-plane key family
  (a `"v1/<family>/"` literal);
- Swift: a file written, or a SQLite file opened;
- web: an IndexedDB database, `localStorage` or Cache Storage.

Each one must do one of these:

- go through a kind constructor (`store.OpenLog`, ...), which takes a Spec. Each `Spec{...}`
  and `Bounds{...}` literal of the store package must set every field that is required;
- declare its kind, its size or count, its age and when they are applied, in a comment on its
  line or just above it:
      // store-bounds: kind=log; size=1GiB; age=90d; applied=hourly sweep
  `age` is at most 90 days, or `until-deleted` for a kind that holds current state;
- say why it is not a store (a temporary file, a script, a file in the person's project):
      // store-bounds: not a store: <why>
- be in scripts/store-bounds-baseline.txt, the stores not moved yet: a count per file and
  opening. The baseline may only shrink: a count above the source's is stale and fails, and
  so does a line or a count that the branch's merge base with origin/main does not have.

`--baseline` writes the baseline with no new entries; `--selftest` checks the check.
"""
import collections
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
BASELINE = "scripts/store-bounds-baseline.txt"
KINDS = {"document", "log", "time series", "graph", "blob", "key-value", "cache", "secret"}
STATE_KINDS = {"document", "graph", "key-value", "secret", "blob"}

GO = [
    (re.compile(r"\bos\.(WriteFile|Create|CreateTemp)\("), None),
    (re.compile(r"\bos\.OpenFile\(.*\bO_CREATE\b"), "os.OpenFile"),
    (re.compile(r"\b([wW]riteAtomically)\("), None),
    (re.compile(r"\b(execfile\.Write|rolllog\.Open|store\.NewFolder)\("), None),
    (re.compile(r'"(v1/[A-Za-z][A-Za-z-]*|runtime/control-[a-z-]+)/?[^"]*"'), None),
]
SWIFT = [
    (re.compile(r"(\.write)\(to:"), "write(to:)"),
    (re.compile(r"\b(createFile)\(atPath:"), None),
    (re.compile(r"\b(sqlite3_open\w*)\("), None),
]
WEB = [(re.compile(r"\b(indexedDB\.open|localStorage\.setItem|caches\.open)\("), None)]
OPENINGS = {".go": GO, ".swift": SWIFT, ".ts": WEB, ".tsx": WEB}

TREES = ("Go/", "App/", "Remote/", "Shared/", "Host/", "Packages/", "Web/src/")
# A key family is a store only where a control plane's store is in hand.
FAMILY_TREES = ("Go/internal/controlplane/", "Go/internal/hosted/")
# The packages whose Spec and Bounds declare a store.
STORE_PACKAGES = ("Go/internal/controlplane/store/", "Go/internal/agentsd/store/")
TEST = re.compile(r"_test\.go$|_gen\.go$|/generated\.ts$|(^|/)\w*Tests?/|/tests?/|\.test\.tsx?$|/testdata/|^Go/internal/(wiretest|flaky)/|^Go/cmd/(wiregen|scale-[\w-]+)/")
NOTE = re.compile(r"//\s*store-bounds:\s*(.*)$")
LITERAL = re.compile(r"\b(?:store\.)?(Spec|Bounds)\{")


def note_problem(note):
    """Why a store-bounds comment does not declare a store, or None."""
    if note.startswith("not a store"):
        return None if re.match(r"not a store: \S", note) else "`not a store` needs a reason"
    fields = {}
    for part in note.split(";"):
        key, sep, value = part.partition("=")
        if not sep or not value.strip():
            return f"cannot read {part.strip()!r}; write key=value"
        fields[key.strip()] = value.strip()
    if fields.get("kind") not in KINDS:
        return "kind must be one of " + ", ".join(sorted(KINDS))
    limits = [fields[k] for k in ("size", "count") if k in fields]
    if not limits:
        return "needs size= or count="
    if not all(re.match(r"[1-9][\d_]*\s*([KMGT]i?B|B)?(\s|$)", v) for v in limits):
        return "size and count must be a number, such as 10MiB or 100_000"
    age = fields.get("age", "")
    m = re.match(r"([1-9]\d*)([dh])$", age)
    if m:
        if int(m[1]) > (90 if m[2] == "d" else 2160):
            return "age must be at most 90d"
    elif not (age == "until-deleted" and fields["kind"] in STATE_KINDS):
        return "age must be <n>d or <n>h, or until-deleted for current state"
    if "applied" not in fields:
        return "needs applied= (when the bounds are applied)"
    return None


def literal_problems(rel, source):
    """Each Spec or Bounds literal of the store package that leaves a required field out."""
    inside = rel.startswith(STORE_PACKAGES)
    if not (inside or any(p.removeprefix("Go/").rstrip("/") + '"' in source for p in STORE_PACKAGES)):
        return []
    found = []
    for m in LITERAL.finditer(source):
        if not inside and not m[0].startswith("store."):
            continue
        if re.match(r"\s*//", source[source.rfind("\n", 0, m.start()) + 1:m.start()]):
            continue
        depth, end = 1, m.end()
        while depth and end < len(source):
            depth += {"{": 1, "}": -1}.get(source[end], 0)
            end += 1
        fields = set(re.findall(r"(?:^|[{,\s])(\w+):", source[m.end():end - 1]))
        need = {"Spec": ["Name", "Kind", "Scope", "Bounds"], "Bounds": ["MaxRecordBytes", "MaxAge"]}[m[1]]
        missing = [f for f in need if f not in fields]
        if m[1] == "Bounds" and not fields & {"MaxCount", "MaxBytes"}:
            missing.append("MaxCount or MaxBytes")
        if missing:
            line = source.count("\n", 0, m.start()) + 1
            found.append(f"{rel}:{line}: {m[1]} leaves out {', '.join(missing)}")
    return found


def openings(rel, source):
    """(the openings with no declaration, as (opening, line)), (the comments that do not say enough)."""
    bare, bad = [], []
    lines = source.splitlines()
    for i, line in enumerate(lines):
        if line.lstrip().startswith(("//", "*", "/*")):
            continue
        hits = []
        for pattern, name in OPENINGS[pathlib.PurePath(rel).suffix]:
            for m in pattern.finditer(line):
                if m[0].startswith('"') and not rel.startswith(FAMILY_TREES):
                    continue
                if re.match(r"\s*func (\([^)]*\) )?$", line[:m.start()]):  # the helper's own declaration
                    continue
                hits.append(name or (m[0][:m.end(1) - m.start()].lstrip('"')))
        if not hits:
            continue
        note = NOTE.search(line)
        j = i - 1
        while not note and j >= 0 and lines[j].lstrip().startswith("//"):
            note = NOTE.search(lines[j])
            j -= 1
        if not note:
            bare += [(h, i + 1) for h in hits]
        elif problem := note_problem(note[1].strip()):
            bad.append(f"{rel}:{i + 1}: store-bounds: {problem}")
    return bare, bad


def read_baseline(text):
    out = {}
    for line in text.splitlines():
        if line and not line.startswith("#"):
            count, key = line.split(" ", 1)
            out[key] = int(count)
    return out


def check(sources, baseline, before=None):
    """The problems, and the count of each bare opening."""
    found, counts, at = [], collections.Counter(), collections.defaultdict(list)
    for rel, source in sorted(sources.items()):
        if rel.endswith(".go"):
            found += literal_problems(rel, source)
        bare, bad = openings(rel, source)
        found += bad
        for name, line in bare:
            counts[f"{rel} {name}"] += 1
            at[f"{rel} {name}"].append(line)
    for key in sorted(counts.keys() | baseline.keys()):
        have, allowed = counts[key], baseline.get(key, 0)
        if have > allowed:
            rel, name = key.split(" ", 1)
            where = ", ".join(map(str, at[key]))
            found.append(f"{rel}:{where}: {name}: {have} with no kind and bounds, baseline {allowed}. Use a kind "
                         "constructor, or add a `// store-bounds:` comment (scripts/check-store-bounds.py says how)")
        elif have < allowed:
            found.append(f"{BASELINE}: {key}: {allowed}, but the source has {have}; "
                         "run scripts/check-store-bounds.py --baseline")
    if before is not None:
        for key, count in sorted(baseline.items()):
            if count > before.get(key, 0):
                found.append(f"{BASELINE}: {key}: {count}, but main has {before.get(key, 0)}; it may only shrink")
    return found, counts


def git(*args):
    p = subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True, check=False)
    return p.stdout if p.returncode == 0 else None


def main_baseline():
    """The baseline at the merge base with origin/main; None before it had one."""
    base = git("merge-base", "HEAD", "origin/main")
    text = git("show", f"{base.strip()}:{BASELINE}") if base else None
    return None if text is None else read_baseline(text)


def product_sources():
    names = git("ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", *TREES)
    if names is None:
        sys.exit("check-store-bounds: git ls-files failed")
    return {n: (ROOT / n).read_text(errors="replace") for n in names.split("\0")
            if pathlib.PurePath(n).suffix in OPENINGS and not TEST.search(n) and (ROOT / n).is_file()}


def selftest():
    def problems(source, rel="Go/internal/a/a.go", baseline=None, before=None):
        return check({rel: source}, baseline or {}, before)[0]

    write = "err := os.WriteFile(p, b, 0o600)"
    key = "Go/internal/a/a.go os.WriteFile"
    good = "// store-bounds: kind=log; size=1GiB; age=90d; applied=hourly sweep\n"
    assert problems(write)
    assert not problems(write, baseline={key: 1})
    assert problems(write + "\n" + write, baseline={key: 1}), "a second opening in a listed file"
    assert problems("", baseline={key: 1}), "stale"
    assert problems(write, baseline={key: 1}, before={}), "the baseline grew"
    assert not problems(write, baseline={key: 1}, before={key: 2})
    assert not problems(good + write)
    assert not problems(write + " " + good)
    assert not problems("// store-bounds: not a store: a script git runs\n" + write)
    assert problems("// store-bounds: not a store\n" + write)
    for part, instead in (("kind=log; ", ""), ("kind=log", "kind=file"), ("size=1GiB; ", ""), ("size=1GiB", "size=big"),
                          ("age=90d; ", ""), ("age=90d", "age=91d"), ("age=90d", "age=until-deleted"),
                          ("; applied=hourly sweep", "")):
        assert problems(good.replace(part, instead) + write), (part, instead)
    assert not problems(good.replace("log", "document").replace("90d", "until-deleted") + write)
    assert problems('const p = "v1/new/"', "Go/internal/controlplane/a.go")
    assert problems('func Key(id string) string { return "v1/new/" + id }', "Go/internal/controlplane/a.go")
    assert not problems("func writeAtomically(p string, data []byte) error {")
    assert not problems('const p = "v1/new/"')
    assert not problems(write, "Go/internal/a/a.go", {key: 1})
    assert problems("try data.write(to: url)", "App/Sources/A.swift")
    assert problems('indexedDB.open("x", 1)', "Web/src/a.ts")
    assert not problems("f, err := os.OpenFile(p, os.O_RDONLY, 0)")
    assert problems("f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0)")
    kinds = "Go/internal/controlplane/store/x.go"
    full = 'Spec{Name: "a", Kind: "log", Scope: "o", Bounds: Bounds{MaxRecordBytes: 1, MaxCount: 1, MaxAge: h}}'
    assert not problems(full, kinds)
    for part in ('Name: "a", ', 'Kind: "log", ', 'Scope: "o", ', "MaxRecordBytes: 1, ", "MaxCount: 1, ", ", MaxAge: h"):
        assert problems(full.replace(part, ""), kinds), part
    outside = 'import "x/internal/controlplane/store"\n'
    assert problems(outside + 'store.OpenLog(store.Spec{Name: "a"}, b)')
    assert problems('import "x/internal/agentsd/store"\n' + 'store.Spec{Name: "a"}')
    assert not problems(outside + "other.Spec{}")
    assert TEST.search("Go/internal/a/a_test.go") and not TEST.search("App/Sources/A.swift")
    print("check-store-bounds: selftest ok")


def main():
    if sys.argv[1:] == ["--selftest"]:
        selftest()
        return 0
    path = ROOT / BASELINE
    baseline = read_baseline(path.read_text())
    sources, before = product_sources(), main_baseline()
    if sys.argv[1:] == ["--baseline"]:
        _, counts = check(sources, {})
        # The first baseline takes all; after that it only shrinks.
        kept = {k: n if before is None else min(n, baseline.get(k, 0)) for k, n in counts.items()}
        head = "# Stores with no declared kind and bounds yet (#6669). It may only shrink: scripts/check-store-bounds.py.\n"
        path.write_text(head + "".join(f"{n} {k}\n" for k, n in sorted(kept.items()) if n))
        return 0
    found, _ = check(sources, baseline, before)
    for problem in found:
        print(problem, file=sys.stderr)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
