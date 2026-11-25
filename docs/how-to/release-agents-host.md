---
diataxis: how-to
devices: [mac]
description: Publish Agent Loops for the Mac on GitHub and the iPhone and iPad app on TestFlight by tagging main, and set up the signing secrets the release needs once.
---

# Release Agent Loops

On a Mac, Agent Loops is one app, a Developer ID download from the folder's GitHub releases. It
is not in the Mac App Store. The iPhone and iPad app (the Remote) goes to TestFlight and
the App Store. Pushing a version tag on main publishes a release. The tag starts the
`release` workflow, which:

- builds Agent Loops, with agentsd and agents-control inside it;
- signs it with Developer ID, notarizes it and staples it, as `Agent-Loops-<version>.dmg`;
- staples the same app and zips it as `Agent-Loops-<version>.zip`, for Macs that cannot
  mount a disk image;
- zips the same signed app again under each old name: `Agents.app`, as
  `Agents-<version>.zip`, and `Agents Host.app`, as `Agents-Host-<version>.zip`. An install
  of the earlier Agents or Agents Host updates itself from its zip. When it runs, agentsd
  renames it to `Agent Loops.app`, one time, so no second app stays (#5297, #4225);
- builds the static Linux agentsd (Go) as `agentsd-linux-<arch>-<version>.tar.gz`, with
  the MCP servers beside it, each file with its `.sha256`. A Linux machine's first install
  takes it when its control plane's server folder has no host for the machine (#5787):
  `install.sh` asks the control plane for its release (`GET /v1/releases/VERSION`),
  downloads the tarball and `SHA256SUMS` from the public releases itself
  (`AGENTS_RELEASE_DOWNLOADS` replaces them, for a test or a mirror), checks the signature
  ("The signing keys", below) and the sum and unpacks it; an install over SSH has the
  control plane take the tarball as a release file (below), check its signature and sum,
  unpack it in its releases folder and copy it over SSH as from its server folder. A
  Linux machine then updates itself from the same tarball (#5788), as a Mac does from its
  zip (below): agentsd downloads `SHA256SUMS`, its signature and `agentsd-linux-<arch>-<version>.tar.gz`
  from its control plane first, and from the public releases when it does not answer,
  checks the signature before it downloads the tarball, then the sum and each program's `.sha256`, asks the new agentsd its `--version`, puts
  it at `bin/agentsd-<sha256>` and the MCP servers beside it between turns, points
  `bin/current` at it in one step and ends with status 75. systemd's `Restart=always`, or
  the supervisor that `--detach` starts (which starts itself again from `bin/current`),
  runs the new copy; a host with neither does not update itself. A new copy that does not
  stay up gives `bin/current` back to the old one. Keep the tarball's names flat, as the
  server folder's;
- creates the release, titled **Agent Loops <version>**, with these files and a
  `SHA256SUMS`, which its `publish` job signs as `SHA256SUMS.sig`. Agent Loops on a Mac updates itself from them (#2178): agentsd downloads
  `SHA256SUMS` and `Agent-Loops-<version>.zip`, first from its control plane
  (`GET /v1/releases/<version>/<file>`), and from the public releases
  (`https://github.com/agentloops-dev/agents-releases/releases/download/v<version>/`, #1064)
  when the control plane does not answer or answers 404. A Linux machine also takes
  `SHA256SUMS.sig`. The control plane takes a file
  from there the first time a machine asks, checks it against that release's `SHA256SUMS`,
  keeps it in its releases folder (`AGENTS_CONTROL_RELEASES`, or `releases` in its
  `--home`), and keeps only its own release and the one before it (#5786).
  `AGENTS_RELEASE_DOWNLOADS` replaces both sources, for a test or a mirror. It checks the sum, then
  the app's signature (the same team and identifier), its notarisation and its
  `CFBundleShortVersionString`. Keep these names. Keep the app in
  `Agent-Loops-<version>.zip` stapled and named `Agent Loops.app`, the app in
  `Agents-<version>.zip` named `Agents.app`, and the app in `Agents-Host-<version>.zip`
  named `Agents Host.app`;
- in its `image` job, builds the control plane's image for `linux/amd64` and `linux/arm64`
  and pushes it to `ghcr.io/agentloops-dev/agents-control` as `<version>`, `<major>.<minor>` and
  `latest`, with an SBOM and provenance, and signs it with cosign, keyless (#2140). It uses
  the workflow's own token: there is no secret to set. The release is not published unless
  the image pushed;
- in its `testflight` job, archives the Remote, checks the archive with
  `scripts/check-store-archive.sh`, uploads it to TestFlight, and waits until App Store
  Connect has processed it. Nothing goes to App Review here.

The program inside the app keeps the name `Agents Host` (`Contents/MacOS/Agents Host`),
because each login item was registered with that path. Do not rename it.

Beside it, the **app-store** workflow (`app-store.yml`, #3657) waits at the `app-store`
environment's gate for a person to approve the release for App Review. Approve it in Agent Loops
(Needs You ▸ **Deploy**, see [Use GitHub](use-github.md#approve-a-waiting-deploy)) or on
GitHub. Then it waits for the processed build, sets "What's New" to the tag's message and
submits the iOS version for App Review. If you reject it, the release stays on TestFlight
only.

The **Release** workflow (`.agents/workflows/release.md`, the `release` skill) does all of
this when you press **Run now**, and nobody needs to watch it. It chooses the version and
writes the notes. The steps below are the same thing by hand.

## Before you start

You set this up once, as the Apple Developer account's Account Holder.

1. **A Developer ID Application certificate.** In Xcode ▸ Settings ▸ Accounts ▸ Manage
   Certificates, click **+** ▸ **Developer ID Application**. Then right-click it and choose
   **Export Certificate…** to save it as a `.p12` with a password.
2. **The `release` environment.** In Keychain Access, under My Certificates, export
   **Developer ID Application: …** as a `.p12`. Then run
   `scripts/release-secrets.sh path/to/DeveloperID.p12` and delete the `.p12`. The script
   creates `release`, allows only tags matching `v*` to deploy to it, and sets these
   secrets (or set them yourself in Settings ▸ Environments):

   | Secret | Value |
   |---|---|
   | `DEVELOPER_ID_P12` | `base64 -i DeveloperID.p12` |
   | `DEVELOPER_ID_P12_PASSWORD` | the password from step 1 |
   | `ASC_KEY_P8` | the App Store Connect API key's `.p8`, as text |
   | `ASC_KEY_ID`, `ASC_ISSUER_ID` | that key's ID and its issuer ID |

   The signing key is one more secret of `release`, `RELEASE_SIGNING_KEY`: "The signing
   keys", below.

3. **The TestFlight secrets.** Only the Remote is in the App Store (#4229). Export **Apple
   Distribution: …** from Keychain Access as a `.p12` file. Install the four App Store
   profiles: **Agents Remote App Store**, **Agents Remote Notify App Store**, **Agents
   Remote Widget App Store** and **Agents Remote Share App Store** (#4915). Then run `scripts/release-secrets.sh --store Distribution.p12`
   and delete the `.p12` file. It sets:

   | Secret | Value |
   |---|---|
   | `APPLE_DISTRIBUTION_P12`, `APPLE_DISTRIBUTION_P12_PASSWORD` | the Apple Distribution identity and its password |
   | `PROFILE_REMOTE` | `base64` of "Agents Remote App Store" |
   | `PROFILE_REMOTE_NOTIFY` | `base64` of "Agents Remote Notify App Store" |
   | `PROFILE_REMOTE_WIDGET` | `base64` of "Agents Remote Widget App Store" |
   | `PROFILE_REMOTE_SHARE` | `base64` of "Agents Remote Share App Store" |

   Without these secrets the GitHub release still goes out, and the `testflight` job fails
   at its first step, naming the secrets that are missing.

   Before it archives, the job runs `scripts/check-store-profiles.py` on the four decoded
   profiles (#2781). Each must allow every entitlement in the `.entitlements` file its target
   signs with in Release. The check names a missing entitlement and the secret, never a
   secret's value. `release-secrets.sh --store` runs the same check on the installed profiles
   before it sets anything.

   **A change that adds an entitlement makes the profiles again.** When a pull request adds a
   capability to an App Store target (Sign in with Apple did, #2507), make that target's
   profile again and install it with `scripts/apple-capability.sh <bundle-id> <capability>`
   (#4284; `--dry-run` first, and a real run only after Alex approves it by a form, because it
   changes the Apple Developer account), and update the `release` environment in
   the same change: `scripts/release-secrets.sh --store …`, or
   `base64 -i <profile> | gh secret set PROFILE_… --env release`. Say so in the pull request.
   Setting the secrets is Alex's to approve.

4. **The App Review gate.** Run `scripts/release-secrets.sh --app-store`. It makes the
   `app-store` environment for `v*` tags, with you as its required reviewer
   (`--app-store <login>` names another), and sets `ASC_KEY_P8`, `ASC_KEY_ID` and
   `ASC_ISSUER_ID` in it from `~/.private_keys`. Without a required reviewer the
   **app-store** workflow submits nothing, and says so in a notice: it never goes to App
   Review without a person's approval.

   App Review also needs the App Store listing in App Store Connect (the description, the
   screenshots, the privacy answers and the age rating) before the first submission. The
   script does not write them. Until they are there, App Store Connect refuses the
   submission and the run's log says what is missing.

## Steps

1. Choose the version, for example `0.2.0`: the next minor, because every release is a
   minor (#3212). `.agents/skills/release/scripts/release-plan.py` says which, and lists
   the merged pull requests. The build number is the workflow's run number, so
   it always goes up.
2. Tag main and push the tag. The tag's message becomes the release notes; a tag without
   one gets notes made from the merged pull requests:

   ```sh
   git tag -a --cleanup=verbatim v0.2.0 origin/main -F notes.md
   git push origin v0.2.0
   ```

3. Follow the **release** run in the repository's Actions. The Linux job takes about ten
   minutes. The Mac job takes longer, mostly waiting for Apple's notary.
4. When it finishes, the release is on the Releases page as **Agent Loops 0.2.0**, and
   0.2.0 (the run number) is on TestFlight for iOS.
   `swift scripts/asc-builds.swift 0.2.0 <run number>` says each build's state.
5. The **app-store** run waits for your approval. When you approve it, App Review gets
   0.2.0. `swift scripts/asc-submit.swift 0.2.0` does the same from this Mac.

To make a release again from a tag that is already there (after fixing a secret, say), run
the **release** workflow by hand and give it the tag. It replaces the release's files.

To make a release again from a tag from before releases were signed, run the workflow of
that tag: `gh workflow run release.yml --ref <tag> -f tag=<tag>`.

## The signing keys

`SHA256SUMS.sig` is the Ed25519 signature, in base64, of the line `agents-release/1` and the
bytes of `SHA256SUMS`. Each file's name in `SHA256SUMS` holds the version, so a signature is
good for one version only. Two public keys are in `Go/internal/releasesig/keys.go` and in
`install.sh` (and `scripts/host-install.sh`, the same text): the **current** key, whose
private half is the `RELEASE_SIGNING_KEY` secret of the `release` environment, and the
**spare** key, whose private half is kept offline. A release signed with either is good.

While the two are the test keys, the `release` workflow stops at its first job (`keys`) and
`scripts/release-host.sh` stops too. The private halves of the test keys are in
`Go/internal/releasesig/testdata`; only a Go test trusts them.

### Make the keys, one time

The person who holds the keys does this. No agent does it.

1. Make an encrypted disk image and open it. Use a password that only you know:

   ```sh
   hdiutil create -size 10m -fs APFS -encryption AES-256 -volname Release-Keys ~/release-keys.dmg
   hdiutil attach ~/release-keys.dmg
   ```

2. Make the two key pairs. Each command shows only the public half:

   ```sh
   scripts/release-key.sh /Volumes/Release-Keys/current.pem
   scripts/release-key.sh /Volumes/Release-Keys/spare.pem
   ```

3. Put the first line in `CurrentKey` and the second in `SpareKey` in
   `Go/internal/releasesig/keys.go`, in quotes. Put the same two lines in `release_keys` in
   `Go/internal/controlplane/install.sh`, and copy that file to `scripts/host-install.sh`.
   `(cd Go && go run ./cmd/release-sign check)` then says nothing. The public halves are
   not secret: an agent can do this step.
4. Put the current private key in the `release` environment:

   ```sh
   scripts/release-secrets.sh --signing-key /Volumes/Release-Keys/current.pem
   ```

5. Copy `release-keys.dmg` to two USB sticks, and keep them in two places. Then close the
   image and take it off the Mac:

   ```sh
   hdiutil detach /Volumes/Release-Keys
   rm ~/release-keys.dmg
   ```

### Replace a key

- **The current key is lost, or somebody else may have it.** Set the spare as the secret
  (`scripts/release-secrets.sh --signing-key …/spare.pem`). Every machine trusts it already.
  Make a new pair. In the next release, `CurrentKey` is the old spare and `SpareKey` is the
  new one. A machine trusts the old current key until it takes that release, so make it
  soon.
- **The spare is lost.** Make a new pair and put it in `SpareKey`. The next release carries it.
- **A planned change** is the same as a lost current key. Keep signing with the old spare
  until no machine has a release from before the change. A machine with two old keys cannot
  take a release that only a newer key signed.
- **Both are lost.** No machine can take an update by itself. Make two new pairs, release,
  and install each Linux machine again with its join command. A Mac is not affected.

Replacing a file of a release (the workflow run again) signs the new `SHA256SUMS`. A control
plane that already kept the old files serves them together, until you remove
`<version>` from its releases folder.

### What is checked, and where it is not

| Download | The check |
|---|---|
| A Linux machine's own update, from its control plane, the public releases or a mirror | The signature, before the tarball is downloaded, opened or run. With no signature at one source, the next source is asked. With a bad one, nothing is installed |
| An install over SSH, from the release | The control plane checks the signature before it unpacks the tarball |
| `install.sh`, from the release | The signature, when the machine has OpenSSL 3. Without it, only the checksum, and the script says so |
| `install.sh` and an install over SSH, from the control plane's server folder (`/v1/servers/`) | The checksum only. These files are the control plane's own, and no signature covers them. The same control plane also sends `install.sh` itself |
| The toolsets (`/v1/toolsets/`) | No signature. The control plane's manifest has the checksum of each download |
| A Mac's update and its first install | Developer ID and the notarisation, as before. Not `SHA256SUMS.sig` |
| The control plane's image | cosign, as before |

A control plane passes `SHA256SUMS.sig` on as it is. One with no internet serves the files
in its releases folder: put `SHA256SUMS`, `SHA256SUMS.sig` and the Linux tarballs in
`<releases folder>/<version>/`.

### A mirror, or your own build

`AGENTS_RELEASE_DOWNLOADS` changes only where the files come from. Their signature is
checked all the same:

- **A mirror of these releases** works with no other setting. Copy `SHA256SUMS.sig` too.
- **Your own build** needs your own key. Make one with `scripts/release-key.sh`, sign with
  `release-sign sign -own -key <private key> <version> <full path of SHA256SUMS>`, and write
  the public half with `release-sign public -pem <private key>`. On each machine, put that
  file where its account owns it and no other account can write it, and set
  `AGENTS_RELEASE_KEY` to its full path in the host's environment (for the systemd user
  service, `systemctl --user edit agents-host` and `Environment=AGENTS_RELEASE_KEY=…`). Set
  it for `install.sh` and for a control plane that installs over SSH too.

`AGENTS_RELEASE_KEY` is one more key beside the two in the program. A file that is missing,
is not a public key, or that another account owns or can write stops every update, and the
host's log says why.

### Older versions

- **An older agentsd** does not ask for the signature. It takes its next update as it did
  before, and checks signatures from that version on.
- **An older control plane** keeps and answers no `SHA256SUMS.sig`, unless the file is in
  its releases folder. A Linux machine then asks the public releases. When they do not
  answer, the machine stays on its version, and its update state says that it could not
  download the signature. Update the control plane first.

## Build one on this Mac

`scripts/release-host.sh 0.2.0` does what the workflow's Mac job does. It needs the
Developer ID identity in your keychain, and `ASC_KEY_ID`,
`ASC_ISSUER_ID` and `ASC_KEY_PATH` set. It needs the Linux agentsd in `App/Resources/servers`,
from `scripts/build-linux-agentsd.sh`, and Go installed (`brew install go`) for the Go
programs Agent Loops carries. It writes `Agent-Loops-0.2.0.dmg`, `Agent-Loops-0.2.0.zip`,
`Agents-0.2.0.zip`, `Agents-Host-0.2.0.zip`, the Linux tarballs and `SHA256SUMS` into `build/release/`.

`scripts/release-host.sh --dry-run 0.2.0` needs none of that. It signs ad hoc and doesn't
notarize, which proves the build and the packaging. Gatekeeper refuses the result.

`scripts/release-store.sh 0.2.0 <build>` does what the `testflight` job does. It needs the
identities and the profiles in step 3, and the App Store Connect key in `~/.private_keys`.
`--no-upload` stops before the upload. `--dry-run` needs no certificate: it proves the
archives build and pass `check-store-archive.sh`.

The `release-dry-run` workflow runs both dry runs on a runner with an empty build cache
(#2780). It starts on a pull request that changes `scripts/build-cache.sh`,
`scripts/release-*.sh`, `release.yml` or `project.yml`, and the release skill's gate runs it on
the commit it will tag: `gh workflow run release-dry-run.yml -f ref=<sha>`.

## If something goes wrong

- **"CurrentKey is a test key":** the tag has no release keys. "The signing keys" says how
  to make them. No file was published.
- **"RELEASE_SIGNING_KEY is not …":** the secret is missing, or is not the private half of a
  key in the tag. Run `scripts/release-secrets.sh --signing-key`, then run the workflow
  again with the tag.
- **"is not on main":** the tag points at a commit main doesn't have. Delete the tag
  (`git push origin :refs/tags/v0.2.0`), then tag a commit on main.
- **No "Developer ID Application" identity:** `DEVELOPER_ID_P12` or its password is wrong.
- **The notary refuses it:** the run's log has the submission ID. Run
  `xcrun notarytool log <id> --key … --key-id … --issuer …` to see why.
- **"The release environment has no …":** a TestFlight secret is missing. Run
  `scripts/release-secrets.sh --store …`, then run the **release** workflow again with the tag.
- **"App Store Connect said 409" in the app-store run:** App Review needs something the
  version does not have yet, often a listing field or an answer for the first version. The
  error names it. Add it in App Store Connect, then re-run the failed job.
- **An upload is refused as a duplicate:** that build number is taken for the version. Run
  the workflow again: the new run number is a new build.
- **"PROFILE_… has no …, which ….entitlements asks for":** the profile in that secret was
  made before the entitlement was added. Make it again and install it with
  `scripts/apple-capability.sh` (Alex approves the real run by a form), run
  `scripts/release-secrets.sh --store …`, then run the **release** workflow again with the tag.
