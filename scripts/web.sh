#!/bin/sh
# The web remote (071): its generated types, its build, and the checks CI runs.
#
#   scripts/web.sh types   regenerate Web/src/protocol/generated.ts: scripts/wire.sh gen (#1135)
#   scripts/web.sh build   install the pinned packages and build Web/dist, as Nx's web:build
#                          (needs Node; with nvm and another Node, nvm installs the pinned
#                          one: #4987)
#   scripts/web.sh dist    what Agents Host's build runs first: build Web/dist when a source is
#                          newer than it; with no Node (or another version), leave an empty one
#                          and warn, so the app still builds and its web remote stays off
#   scripts/web.sh test    the web app's tests, as Nx's web:test (needs Node)
#   scripts/web.sh check   everything CI checks: types fresh (scripts/wire.sh check), and, when
#                          Node is here, a build,
#                          Web/dist matching its manifest, and Nx's web:lint (tsc, lint,
#                          licences) and web:test
#
# generated.ts is checked in, made from Packages/Wire: a change to a protocol type changes the
# schema there, regenerates and commits it. Web/dist
# is not (#473): it is ignored, built here, and a pull request never carries it.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

. "$root/scripts/node.sh"

# Web/dist is older than one of the inputs build.mjs hashes, or was never built.
dist_stale() {
	[ -f Web/dist/MANIFEST ] || return 0
	[ -n "$(find Web/src Web/assets Web/index.html Web/sandbox.html Web/build.mjs Web/lucide.mjs Web/tsconfig.json \
		Web/package.json Web/.node-version package.json pnpm-lock.yaml -newer Web/dist/MANIFEST 2>/dev/null | head -1)" ]
}

# Web/dist through Nx (#5628): a cache hit puts back the dist that the same inputs built.
build() {
	"$root/scripts/nx.sh" run web:build "$@"
}

case "${1:-}" in
types)
	exec scripts/wire.sh gen
	;;
build)
	use_pinned_node install
	need_node build
	build
	;;
dist)
	if ! dist_stale; then
		exit 0
	elif use_pinned_node && have_pinned_node; then
		# Not wire:gen and design:gen first: their files are checked in, and an Xcode build
		# may have no Go.
		build --exclude-task-dependencies
	else
		# "warning:" is how an Xcode script phase shows one in the build log.
		echo "warning: web.sh: no Node $(cat Web/.node-version) here, so Agents Host is built without its web remote; install it and build again to serve the web page"
		mkdir -p Web/dist
	fi
	;;
test)
	use_pinned_node install
	need_node test
	exec "$root/scripts/nx.sh" run web:test
	;;
check)
	scripts/wire.sh check
	use_pinned_node install
	if have_pinned_node; then
		"$root/scripts/nx.sh" run-many -p web -t build lint test
		scripts/build-cache.sh swift test --package-path Packages/AgentsKit --filter 'WebDistManifestTests|ControlAgreementVectorTests'
	else
		scripts/build-cache.sh swift test --package-path Packages/AgentsKit --filter 'ControlAgreementVectorTests'
		echo "web.sh: no Node here; checked the types only (CI's go job builds and tests it with Node)" >&2
	fi
	;;
*)
	echo "usage: scripts/web.sh types | build | dist | test | check" >&2
	exit 2
	;;
esac
