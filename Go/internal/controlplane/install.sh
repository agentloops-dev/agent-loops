#!/bin/sh
# Make this machine a host of an Agents control plane (058, US4).
#
#   curl -fsSL [--pinnedpubkey sha256//…] https://<control plane>/v1/install.sh | sh -s -- '<host code>' [name]
#   curl … | sh -s -- --join '<url>' '<pin or ->' '<person>' [name]
#
# With --join there is no code: the host asks to join as the person, shows a short code,
# and waits until the person approves it on a client (#994).
#
# Installs the host (agentsd, Go since #1136) for this user in ~/.agents-server, from the
# control plane, or from the public releases when it has no host for this machine (#5787). With a
# host code, `agentsd install --join` then sets the machine up and starts the host (#5765);
# with --join, this script starts it. It connects out to the control plane; nothing needs
# to reach this machine. Run it again with a new code to join again. The Swift host, and
# --impl, went in #1381.
set -eu

code= name= ask=
if [ "${1:-}" = --join ]; then
    [ $# -ge 4 ] || { echo "Say the control plane's address, its pin (or -) and the person." >&2; exit 2; }
    ask=1 url=$2 pin=$3 person=$4 name=${5:-}
    case "$url" in https://*) ;; *) echo "The control plane's address starts with https://." >&2; exit 2 ;; esac
else
    while [ $# -gt 0 ]; do
        if [ -z "$code" ]; then code=$1; elif [ -z "$name" ]; then name=$1; fi; shift
    done
    [ -n "$code" ] || { echo "Say the host code the control plane showed." >&2; exit 2; }
    case "$code" in agents-control:[234]:h:*) ;; *) echo "That is not a host code." >&2; exit 2 ;; esac
    # The code's own fields: agents-control:2:h:-:<key>:<secret>:<url>:<pin>:<name>, and in a
    # version 3 or 4 code more after them (a person key, an invite: #3491).
    url=$(printf %s "$code" | cut -d: -f7 | sed 's/%3[Aa]/:/g; s/%2[Ff]/\//g; s/%25/%/g')
    pin=$(printf %s "$code" | cut -d: -f8)
fi
name=${name:-$(hostname)}

fetch() {
    if [ "$pin" != "-" ]; then
        command -v curl >/dev/null 2>&1 || { echo "curl is needed to check the control plane's certificate." >&2; exit 1; }
        # The pin is base64url; curl wants base64 with its padding.
        b64=$(printf %s "$pin" | tr '_-' '/+')
        while [ $(( ${#b64} % 4 )) -ne 0 ]; do b64="$b64="; done
        curl -fsS --insecure --pinnedpubkey "sha256//$b64" -o "$2" "$url$1"
    else
        # A publicly trusted certificate: the system's own roots have to be there.
        if [ ! -e /etc/ssl/certs/ca-certificates.crt ] && [ ! -d /etc/ssl/certs ] && [ ! -e /etc/pki/tls/certs/ca-bundle.crt ]; then
            echo "This machine has no CA certificates to check the control plane with. Install ca-certificates first." >&2
            exit 1
        fi
        if command -v curl >/dev/null 2>&1; then curl -fsS -o "$2" "$url$1"; else wget -q -O "$2" "$url$1"; fi
    fi
}

case "$(uname -s)" in Linux) ;; *) echo "Hosts other than Macs run Linux." >&2; exit 1 ;; esac
case "$(uname -m)" in
    x86_64|amd64) arch=x86_64 ;;
    aarch64|arm64) arch=aarch64 ;;
    *) echo "$(uname -m) is not a machine the host is built for." >&2; exit 1 ;;
esac

d="$HOME/.agents-server"
umask 077
mkdir -p "$d/bin" "$d/root"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# A file of the host: from the control plane's server folder, or, when that has no host for
# this machine, from the release unpacked in $from.
from=
get() {
    if [ -n "$from" ]; then cp "$from/${1#/v1/servers/}" "$2" 2>/dev/null; else fetch "$@"; fi
}
# The public releases (#5787, epic #5785). GitHub's certificate is public: the system's own
# roots check it. AGENTS_RELEASE_DOWNLOADS replaces them, for a test or a mirror.
releases=${AGENTS_RELEASE_DOWNLOADS:-https://github.com/agentloops-dev/agents-releases/releases/download}
public() {
    if command -v curl >/dev/null 2>&1; then curl -fsSL -o "$2" "$1"; else wget -q -O "$2" "$1"; fi
}

# The release's public keys, as Go/internal/releasesig/keys.go has them (a test keeps them
# equal): the current one and the spare.
release_keys="MCowBQYDK2VwAyEAbK/TkG4B9kEq3EoU3wxL+UOmjOjCTaVMqvJQ6xIryhg=
MCowBQYDK2VwAyEAW3GLYtpZB4egkA88jeiabbtP9ynkRMhM/1sU5w4vP60="
# check_signature stops when no release key, nor the one in the file AGENTS_RELEASE_KEY names,
# signed $tmp/SHA256SUMS. OpenSSL 3 is the first that reads such a signature: with none, it
# says so and goes on, or stops when AGENTS_RELEASE_REQUIRE_SIGNATURE is 1.
check_signature() {
    if ! { command -v openssl >/dev/null 2>&1 && openssl pkeyutl -help 2>&1 | grep -q -e '-rawin'; }; then
        echo "This machine has no OpenSSL 3, so release $version's signature is not checked, only its checksum." >&2
        [ "${AGENTS_RELEASE_REQUIRE_SIGNATURE:-}" != 1 ] || exit 1
        return 0
    fi
    public "$releases/v$version/SHA256SUMS.sig" "$tmp/SHA256SUMS.sig" 2>/dev/null ||
        { echo "Release $version's signature could not be downloaded." >&2; exit 1; }
    { printf 'agents-release/1\n'; cat "$tmp/SHA256SUMS"; } > "$tmp/signed"
    openssl base64 -d -A -in "$tmp/SHA256SUMS.sig" -out "$tmp/signature" 2>/dev/null || : > "$tmp/signature"
    for key in $release_keys; do
        printf '%s\n' '-----BEGIN PUBLIC KEY-----' "$key" '-----END PUBLIC KEY-----' > "$tmp/key.pem"
        openssl pkey -pubin -in "$tmp/key.pem" -noout >/dev/null 2>&1 ||
            { echo "This machine's OpenSSL does not check Ed25519 signatures (in FIPS mode it may not)." >&2; exit 1; }
        if signed_by "$tmp/key.pem"; then echo "Release $version's signature is good."; return 0; fi
    done
    if [ -n "${AGENTS_RELEASE_KEY:-}" ] && signed_by "$AGENTS_RELEASE_KEY"; then
        echo "Release $version's signature is good, by AGENTS_RELEASE_KEY."
        return 0
    fi
    echo "The download did not match its signature." >&2
    exit 1
}
signed_by() {
    openssl pkeyutl -verify -pubin -inkey "$1" -rawin -in "$tmp/signed" -sigfile "$tmp/signature" >/dev/null 2>&1
}
# The same rule as agentsd's: a file that this account or root owns, and no other can write.
if [ -n "${AGENTS_RELEASE_KEY:-}" ]; then
    [ -f "$AGENTS_RELEASE_KEY" ] && [ -r "$AGENTS_RELEASE_KEY" ] &&
        [ -n "$(find -L "$AGENTS_RELEASE_KEY" -prune \( -user "$(id -u)" -o -user 0 \) ! -perm -020 ! -perm -002 2>/dev/null)" ] ||
        { echo "AGENTS_RELEASE_KEY names no key file that only this account can write." >&2; exit 1; }
fi

echo "Fetching the host for $arch-linux from $url…"
if ! fetch "/v1/servers/agentsd-linux-$arch.sha256" "$tmp/sha256" 2>/dev/null; then
    # No host for this machine on the control plane: the same files of the release it
    # names, from the public releases, checked against that release's SHA256SUMS.
    version=
    fetch /v1/releases/VERSION "$tmp/VERSION" 2>/dev/null && version=$(tr -d ' \n' < "$tmp/VERSION")
    case "$version" in
        [0-9]*.[0-9]*.[0-9]*) ;;
        *) echo "This control plane offers no host for $arch-linux." >&2; exit 1 ;;
    esac
    tarball="agentsd-linux-$arch-$version.tar.gz"
    echo "This control plane has no host for $arch-linux; fetching release $version from $releases…"
    { public "$releases/v$version/SHA256SUMS" "$tmp/SHA256SUMS" && public "$releases/v$version/$tarball" "$tmp/$tarball"; } ||
        { echo "The releases offer no host $version for $arch-linux." >&2; exit 1; }
    check_signature
    want=$(awk -v f="$tarball" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
    [ -n "$want" ] && echo "$want  $tmp/$tarball" | sha256sum -c - >/dev/null ||
        { echo "The download did not match its checksum." >&2; exit 1; }
    mkdir "$tmp/release"
    tar -xzf "$tmp/$tarball" -C "$tmp/release"
    from="$tmp/release"
    get "/v1/servers/agentsd-linux-$arch.sha256" "$tmp/sha256" ||
        { echo "Release $version has no host for $arch-linux." >&2; exit 1; }
else
    echo "The host comes from the control plane's own files: its checksum is checked, and no release signature covers it."
fi
get "/v1/servers/agentsd-linux-$arch" "$tmp/agentsd"
sha=$(tr -d ' \n' < "$tmp/sha256")
echo "$sha  $tmp/agentsd" | sha256sum -c - >/dev/null || { echo "The download did not match its checksum." >&2; exit 1; }
chmod 700 "$tmp/agentsd"
mv "$tmp/agentsd" "$d/bin/agentsd-$sha"
ln -sfn "agentsd-$sha" "$d/bin/current"

# The MCP servers Agents ships (#700), beside agentsd, which puts its own folder first on
# their PATH, from the same place as the host. One that is not there is left out, with a word.
for server in github-mcp linear-mcp gitlab-mcp email-mcp jira-mcp slack-mcp googlechat-mcp whatsapp-mcp sentry-mcp betterstack-mcp forgejo-mcp bitbucket-mcp azuredevops-mcp azureboards-mcp; do
    if get "/v1/servers/$server-linux-$arch.sha256" "$tmp/$server.sha256" 2>/dev/null &&
        get "/v1/servers/$server-linux-$arch" "$tmp/$server" 2>/dev/null &&
        echo "$(tr -d ' \n' < "$tmp/$server.sha256")  $tmp/$server" | sha256sum -c - >/dev/null 2>&1; then
        chmod 700 "$tmp/$server"
        mv "$tmp/$server" "$d/bin/$server"
    else
        echo "There is no $server for $arch-linux; the host runs without it." >&2
    fi
done

# The Node toolsets the control plane carries (#1312): the Go host installs Claude from
# them by itself, and Codex when asked, into ~/.agents-server/tools.
offered=
for runtime in claude codex; do
    mkdir -p "$tmp/toolsets/$runtime"
    ok=1
    for file in manifest.json package.json package-lock.json; do
        fetch "/v1/toolsets/$runtime/$file" "$tmp/toolsets/$runtime/$file" 2>/dev/null || { ok=0; break; }
    done
    if [ $ok = 1 ]; then
        mkdir -p "$d/toolsets"
        rm -rf "$d/toolsets/$runtime"
        mv "$tmp/toolsets/$runtime" "$d/toolsets/$runtime"
        offered="$offered $runtime"
    fi
done
[ -n "$offered" ] || echo "This control plane offers no toolsets; the host installs no Claude by itself." >&2

# The agents' browser on Linux (#5209, #5228): Google Chrome stable, headed under Xvfb,
# which Google sign-in and Cloudflare let in where they refuse Chrome for Testing
# headless. It is installed only with root, or sudo that asks no password, on Debian or
# Ubuntu x86_64 (Google ships no Chrome for Linux arm64). Otherwise nothing is installed,
# the browser stays Chrome for Testing headless, and one line says why. Chrome then
# updates from Google's apt repository, as on any desktop. This step never stops the
# install. install.sh holds the same text (a test keeps them equal).
install_browser() {
    if command -v google-chrome-stable >/dev/null 2>&1 && command -v Xvfb >/dev/null 2>&1; then
        return 0
    fi
    headless="The agents' browser here stays Chrome for Testing headless:"
    case "$(uname -m)" in
        x86_64|amd64) ;;
        *) echo "$headless Google ships no Chrome for Linux $(uname -m)."; return 0 ;;
    esac
    if [ ! -f /etc/debian_version ] || ! command -v apt-get >/dev/null 2>&1; then
        echo "$headless Agents installs Google Chrome and Xvfb only on Debian or Ubuntu."
        return 0
    fi
    if [ "$(id -u)" = 0 ]; then
        as=
    elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
        as="sudo -n"
    else
        echo "$headless installing Google Chrome and Xvfb needs root, or sudo with no password."
        return 0
    fi
    echo "Installing Google Chrome and Xvfb for the agents' browser…"
    # Enough for the first install. Chrome's package then writes its own source,
    # google-chrome.sources, signed by its own keyring, and removes this .list.
    key=$(mktemp) || return 0
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$key" https://dl.google.com/linux/linux_signing_key.pub
    else
        wget -q -O "$key" https://dl.google.com/linux/linux_signing_key.pub
    fi || { rm -f "$key"; echo "$headless Google's signing key could not be fetched."; return 0; }
    if ! grep -q "BEGIN PGP PUBLIC KEY BLOCK" "$key"; then
        rm -f "$key"; echo "$headless Google's signing key was not a key."; return 0
    fi
    $as install -D -m 644 "$key" /etc/apt/trusted.gpg.d/google-chrome.asc || { rm -f "$key"; return 0; }
    rm -f "$key"
    echo "deb [arch=amd64] https://dl.google.com/linux/chrome/deb/ stable main" |
        $as tee /etc/apt/sources.list.d/google-chrome.list >/dev/null || return 0
    if $as env DEBIAN_FRONTEND=noninteractive apt-get -qq update >/dev/null &&
        $as env DEBIAN_FRONTEND=noninteractive apt-get -qq install -y google-chrome-stable xvfb >/dev/null; then
        # Its source names its own keyring, so the key above need not be trusted for
        # every repository.
        if [ -f /etc/apt/sources.list.d/google-chrome.sources ]; then
            $as rm -f /etc/apt/trusted.gpg.d/google-chrome.asc
        fi
        echo "Installed $(google-chrome-stable --version 2>/dev/null | sed 's/ *$//') and Xvfb."
    else
        echo "$headless apt-get could not install Google Chrome and Xvfb."
    fi
    return 0
}
install_browser || true

# A host already running here stops; it is started again below with the new code.
if [ -f "$d/root/daemon.lock" ]; then
    old=$(cat "$d/root/daemon.lock" 2>/dev/null || true)
    [ -n "$old" ] && kill "$old" 2>/dev/null || true
    sleep 1
fi
# The host enrols again with the new code. (The record of which host was installed,
# from when there were two, is gone with them.)
rm -f "$d/root/control-host.json" "$d/impl" "$d/root/control-join-shown"
if [ -z "$ask" ]; then
    # With a host code, the one installer (#5356, #5765) does the rest: it joins with the
    # code, starts the host, installs the runtimes and prepares the projects, and says each
    # step of this machine's set-up, which the machine's page shows. Safe to run again.
    rm -f "$d/root/control-join-ask"
    exec "$d/bin/current" install --join "$code" --name "$name"
fi
# The person's id or name, the address and the pin, as JSON: none holds a quote.
for v in "$url" "$pin" "$person"; do
    case "$v" in *'"'*|*'\'*) echo "The address, pin and person can't hold quotes." >&2; exit 2 ;; esac
done
printf '{"url":"%s","pin":"%s","person":"%s"}' "$url" "$pin" "$person" > "$d/root/control-join-ask"
chmod 600 "$d/root/control-join-ask"
rm -f "$d/root/control-join-code"

unit="$HOME/.config/systemd/user/agents-host.service"
# /run/systemd/system is there only when systemd runs (sd_booted). A codespace's
# systemctl is a shim that exits 0 with no systemd, so its exit code alone says
# nothing (#3366).
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
    mkdir -p "$(dirname "$unit")"
    cat > "$unit" <<UNIT
[Unit]
Description=Agent Loops host ($name)
After=network-online.target

[Service]
ExecStart=%h/.agents-server/bin/current --root %h/.agents-server/root --serve --control-network --host-name "$name"
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
UNIT
    systemctl --user daemon-reload
    systemctl --user enable agents-host.service >/dev/null
    systemctl --user restart agents-host.service
    echo "Started as the systemd user service agents-host."
    echo "To keep it running after you log out: loginctl enable-linger $(id -un)"
else
    "$d/bin/current" --root "$d/root" --serve --detach --control-network --host-name "$name"
    echo "Started in the background (no systemd user session here)."
fi

# Up to the request's 10 minutes, and a little more.
wait=660
i=0
while [ ! -f "$d/root/control-join-shown" ] && [ $i -lt 30 ]; do sleep 1; i=$((i + 1)); done
if [ -f "$d/root/control-join-shown" ]; then
    echo "Waiting for approval. On a client of $person, approve $name. Check that it shows the code $(cat "$d/root/control-join-shown")."
fi
i=0
while [ ! -f "$d/root/control-host.json" ] && [ $i -lt $wait ]; do sleep 1; i=$((i + 1)); done
if [ -f "$d/root/control-host.json" ]; then
    echo "$name joined the control plane."
else
    echo "The host started but has not joined yet. Its log: $d/root/daemon.log" >&2
    exit 1
fi