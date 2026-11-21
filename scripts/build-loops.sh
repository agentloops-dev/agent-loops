#!/bin/zsh
# Build loops, the command-line client (epic #6443), for the release's downloads (#6448):
# one tarball per system and architecture, each with the one program `loops` in it.
#
#   scripts/build-loops.sh VERSION OUT              build, sign with Developer ID, notarize
#   scripts/build-loops.sh --dry-run VERSION OUT    build and sign ad hoc: no certificate,
#                                                   no notarization
#
# What it writes into OUT:
#   loops-darwin-arm64-<version>.tar.gz   macOS on Apple silicon
#   loops-darwin-amd64-<version>.tar.gz   macOS on Intel
#   loops-linux-arm64-<version>.tar.gz    Linux, static
#   loops-linux-amd64-<version>.tar.gz    Linux, static
#
# Go cross-compiles each one with cgo off (Go/scripts/go.sh): lease "build" first. The macOS
# programs are signed with the hardened runtime and notarized as one zip, so Gatekeeper lets
# a downloaded one run; a bare program has no ticket to staple, so Gatekeeper checks it on
# line. Notarizing needs ASC_KEY_ID, ASC_ISSUER_ID and ASC_KEY_PATH, as release-host.sh does.
# Agent Loops.app carries its own loops in Contents/Helpers (scripts/go-github-mcp.sh loops).
set -euo pipefail

root=${0:A:h:h}
dry=0
[[ ${1:-} == --dry-run ]] && { dry=1; shift }
version=${1:?usage: build-loops.sh [--dry-run] VERSION OUT}
out=${2:?usage: build-loops.sh [--dry-run] VERSION OUT}
mkdir -p $out
out=${out:A}
work=$root/Go/out/loops
rm -rf $work
mkdir -p $work

for goos goarch in darwin arm64 darwin amd64 linux arm64 linux amd64; do
    print "building loops for $goos-$goarch…"
    mkdir -p $work/$goos-$goarch
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch $root/Go/scripts/go.sh build -trimpath \
        -ldflags "-s -w -X github.com/agentloops-dev/agent-loops/Go/internal/buildinfo.Version=$version" \
        -o out/loops/$goos-$goarch/loops ./cmd/loops
done

for goarch in arm64 amd64; do
    if (( dry )); then
        codesign --force --options runtime --sign - $work/darwin-$goarch/loops
    else
        codesign --force --options runtime --timestamp --sign "Developer ID Application" \
            $work/darwin-$goarch/loops
    fi
    codesign --verify --strict $work/darwin-$goarch/loops
done

if (( ! dry )); then
    : ${ASC_KEY_ID:?} ${ASC_ISSUER_ID:?} ${ASC_KEY_PATH:?}
    zip=$work/loops-darwin.zip
    (cd $work && zip -qr $zip darwin-arm64 darwin-amd64)
    print "notarizing loops…"
    xcrun notarytool submit $zip --key $ASC_KEY_PATH --key-id $ASC_KEY_ID \
        --issuer $ASC_ISSUER_ID --wait --timeout 30m
    rm -f $zip
fi

for goos goarch in darwin arm64 darwin amd64 linux arm64 linux amd64; do
    tar -czf $out/loops-$goos-$goarch-$version.tar.gz -C $work/$goos-$goarch loops
done
print "wrote loops $version for macOS and Linux (arm64, amd64) into $out"
