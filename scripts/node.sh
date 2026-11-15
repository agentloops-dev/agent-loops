# shellcheck shell=sh
# The pinned Node (Web/.node-version), for scripts/web.sh, scripts/pnpm.sh and scripts/nx.sh
# (#5628). Source it with $root set to the repository; it defines functions and runs nothing.
: "${root:?node.sh needs root, the repository}"
#
#   use_pinned_node [install]   put the pinned Node first on PATH, when this host has it (with
#                               "install", nvm downloads it when it is missing)
#   have_node, have_pinned_node, need_node WHAT

# An Xcode script phase has a bare PATH: look where Node is usually installed as well.
PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"

have_node() {
	command -v node >/dev/null 2>&1
}

need_node() {
	have_pinned_node || { echo "$(basename -- "$0"): $1 needs Node $(cat "$root/Web/.node-version")" >&2; exit 1; }
}

# The pinned Node, not just any: build.mjs refuses another, as its bytes would differ.
have_pinned_node() {
	have_node && [ "$(node --version)" = "v$(cat "$root/Web/.node-version")" ]
}

# A host with nvm and another Node (a codespace's image has Node 24; #4987): put the pinned
# Node first on PATH. With "install", nvm downloads it when it is missing; without, only a
# Node that nvm already has is used, so an Xcode build phase never downloads. nvm is a bash
# function that does not run under `set -u`, so it runs in a bash of its own.
use_nvm_node() {
	have_pinned_node && return 0
	nvm_sh="${NVM_DIR:-$HOME/.nvm}/nvm.sh"
	[ -s "$nvm_sh" ] && command -v bash >/dev/null 2>&1 || return 0
	version=$(cat "$root/Web/.node-version")
	if [ "${1:-}" = install ]; then
		node_bin=$(NVM_SH="$nvm_sh" V="$version" bash -c '. "$NVM_SH" >/dev/null 2>&1 &&
			nvm install "$V" >&2 && nvm which "$V"') || return 0
	else
		node_bin=$(NVM_SH="$nvm_sh" V="$version" bash -c '. "$NVM_SH" >/dev/null 2>&1 &&
			nvm which "$V"' 2>/dev/null) || return 0
	fi
	[ -x "$node_bin" ] || return 0
	PATH="$(dirname -- "$node_bin"):$PATH"
	export PATH
}

# A Mac whose first `node` on PATH is another version, but which has the pinned one installed as
# well (an agent session puts its runtime's own Node first; #4904): put that one first. Homebrew
# keeps each version under Cellar/node/<version>; its bin is checked last, as it may be a link
# to another version.
use_installed_node() {
	have_pinned_node && return 0
	version=$(cat "$root/Web/.node-version")
	for dir in /opt/homebrew/Cellar/node/"$version"/bin /usr/local/Cellar/node/"$version"/bin \
		/opt/homebrew/bin /usr/local/bin; do
		[ -x "$dir/node" ] || continue
		[ "$("$dir/node" --version 2>/dev/null)" = "v$version" ] || continue
		PATH="$dir:$PATH"
		export PATH
		return 0
	done
	return 0
}

# The pinned Node from nvm, or else from where it is usually installed.
use_pinned_node() {
	use_nvm_node "$@"
	use_installed_node
}
