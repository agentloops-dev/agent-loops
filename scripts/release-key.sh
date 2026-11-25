#!/bin/zsh
# Make one key pair that signs a release's SHA256SUMS (docs/how-to/release-agents-host.md,
# "The signing keys").
#
#   scripts/release-key.sh /Volumes/Release-Keys/current.pem
#   scripts/release-key.sh /Volumes/Release-Keys/spare.pem
#
# The private half goes into that new file, which only this account can read. It is never
# shown, sent or put anywhere else. The script shows the public half, one line of base64,
# which is the line for Go/internal/releasesig/keys.go and install.sh. It writes over no
# file, and makes no key in a git checkout.
#
# Lease "build" first: it builds cmd/release-sign.
set -euo pipefail

root=${0:A:h:h}
file=${1:?usage: release-key.sh FILE}
file=${file:a}
[[ -d ${file:h} ]] || {
    print -u2 "There is no folder ${file:h}. If it is the key image, open it first: hdiutil attach ~/release-keys.dmg"
    exit 1
}
[[ -e $file || -L $file ]] && { print -u2 "$file is there already."; exit 1 }
if git -C ${file:h} rev-parse --git-dir >/dev/null 2>&1; then
    print -u2 "${file:h} is in a git checkout: a private key does not go there."
    exit 1
fi
umask 077
public=$($root/Go/scripts/go.sh run ./cmd/release-sign new $file)
print "The private key is in $file."
print "The public key:"
print -r -- $public
