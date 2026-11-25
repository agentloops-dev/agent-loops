#!/bin/sh
# Make a release's Linux tarball for one architecture: agentsd, agents-control, the MCP servers
# Agent Loops ships (#700), each beside its .sha256, VERSION, and web/, the built web page that
# `agentsd install --control` puts in place (#7288). release-host.sh runs it, and
# Go/internal/controlsetup's test installs from what it makes.
#
#   scripts/release-linux-tarball.sh SERVERS DIST ARCH OUT.tar.gz
set -eu

[ $# = 4 ] || { echo "usage: release-linux-tarball.sh SERVERS DIST ARCH OUT.tar.gz" >&2; exit 2; }
servers=$(CDPATH= cd -- "$1" && pwd)
dist=$2
arch=$3
out=$4

# Agent Loops' build leaves an empty Web/dist when it has no Node (scripts/web.sh dist): a
# release must not go out without the page.
grep -q ' Web/dist/index.html$' "$dist/MANIFEST" 2>/dev/null || {
	echo "No web page in $dist: run scripts/web.sh build first." >&2
	exit 1
}
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cp -R "$dist" "$stage/web"

set --
for program in agentsd agents-control github-mcp linear-mcp gitlab-mcp email-mcp jira-mcp slack-mcp \
	googlechat-mcp whatsapp-mcp sentry-mcp betterstack-mcp forgejo-mcp bitbucket-mcp azuredevops-mcp \
	azureboards-mcp; do
	set -- "$@" "$program-linux-$arch" "$program-linux-$arch.sha256"
done
# No ._ files from a Mac's extended attributes.
COPYFILE_DISABLE=1 tar -czf "$out" -C "$servers" "$@" VERSION -C "$stage" web
