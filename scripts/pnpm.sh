#!/bin/sh
# The pinned pnpm (#5628), with the pinned Node first on PATH (scripts/node.sh).
#
#   scripts/pnpm.sh install --frozen-lockfile
#   scripts/pnpm.sh exec nx run web:test
#
# The version and the SHA-512 of its npm tarball are the root package.json's `packageManager`
# (pnpm@<version>+sha512.<hex>). The first run downloads that tarball, refuses it when its hash
# is another, and keeps it in a cache that every worktree shares: on a Mac
# ~/Library/Caches/Agents-build/pnpm (AGENTS_BUILD_CACHE moves the parent), elsewhere
# ~/.cache/agents-pnpm. pnpm's own wrapper then downloads its native binary once, beside it,
# and checks the registry's signature on it. Node 25 has no Corepack, so nothing else pins it.
# Dependabot bumps the pin (.github/dependabot.yml); a bump changes the hash too.
#
# Never install without --frozen-lockfile, and never let a package's install script run
# (pnpm-workspace.yaml's allowBuilds; epic #5627, "Pin and wait"). Lease "build" first.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$root/scripts/node.sh"
use_pinned_node install
need_node pnpm

spec=$(node -p 'require(process.argv[1]).packageManager' "$root/package.json")
version=${spec#pnpm@}
version=${version%%+*}
want=${spec##*+sha512.}
case "$spec" in
pnpm@*+sha512.*) ;;
*)
	echo "pnpm.sh: package.json's packageManager is $spec, not pnpm@<version>+sha512.<hex>" >&2
	exit 1
	;;
esac

if [ "$(uname -s)" = Darwin ] && [ -z "${CI:-}" ]; then
	cache="${AGENTS_BUILD_CACHE:-$HOME/Library/Caches/Agents-build}/pnpm"
else
	cache="${XDG_CACHE_HOME:-$HOME/.cache}/agents-pnpm"
fi
dir="$cache/$version"
if [ ! -f "$dir/package/bin/pnpm.mjs" ]; then
	mkdir -p "$cache"
	tmp=$(mktemp -d "$cache/.download.XXXXXX")
	trap 'rm -rf "$tmp"' EXIT
	# Node fetches and hashes it, so this needs no curl or openssl (the web image has neither).
	node -e '(async () => {
		const [url, file, want] = process.argv.slice(1);
		const res = await fetch(url);
		if (!res.ok) { console.error(`pnpm.sh: ${url}: ${res.status}`); process.exit(1); }
		const bytes = Buffer.from(await res.arrayBuffer());
		const got = require("node:crypto").createHash("sha512").update(bytes).digest("hex");
		if (got !== want) { console.error(`pnpm.sh: ${url} has SHA-512 ${got}, not the ${want} that package.json pins`); process.exit(1); }
		require("node:fs").writeFileSync(file, bytes);
	})()' "https://registry.npmjs.org/pnpm/-/pnpm-$version.tgz" "$tmp/pnpm.tgz" "$want"
	tar -xzf "$tmp/pnpm.tgz" -C "$tmp"
	mkdir -p "$dir"
	# Another worktree may have put it there first: then its copy is the same bytes.
	mv "$tmp/package" "$dir/package" 2>/dev/null || [ -f "$dir/package/bin/pnpm.mjs" ]
fi
exec node "$dir/package/bin/pnpm.mjs" "$@"
