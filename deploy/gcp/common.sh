#!/usr/bin/env bash
# shellcheck disable=SC2034 # the names are used by up.sh and down.sh, which source this
# What deploy/gcp/up.sh and down.sh share (#1461): the names of what they make, the service
# account key they act as, and --dry-run. Sourced, not run.
#
# gcloud runs with a configuration folder of its own, ~/.agents/gcloud/agents-control, signed in
# as the key file's service account, so the gcloud configuration of the person who runs it is
# not touched.

key_file=${AGENTS_GCP_KEY_FILE:-$HOME/.agents/secrets/gcp-agents-control.json}
# The free tier has one e2-micro VM and 30 GB of standard disk in us-west1, us-central1 or us-east1.
region=us-west1
zone=us-west1-b
name=agents-control
subnet_range=10.61.0.0/24
vm_account_name=agents-control-vm
keyring=agents
key_location=global
key=control
# The VM's disk is snapshotted every day, and each snapshot kept 7 days (#2136). The boot
# disk has the VM's name. 10:00 UTC is 03:00 in us-west1, when the control plane is quiet.
snapshots=$name-daily
snapshot_days=7
snapshot_start=10:00
dry_run=0
project=''
vm_account=''
# The control plane's image (#2140): a release pushes ghcr.io/<the repo's owner>/agents-control:X.Y.Z
# (release.yml, #4958), and up.sh --build tags its own build the same way. up.sh reads the owner
# with image_of_repo (#5472), so a move of the repo cannot leave the deploy behind;
# deploy/gcp/hosted-up.sh builds its own image here, with this name, as compose.hosted.yaml says.
image=ghcr.io/agentloops-dev/agents-control
build=0

say() { printf '%s\n' "$*"; }
die() {
	printf '%s: %s\n' "${0##*/}" "$*" >&2
	exit 1
}

# shown CMD…: CMD as one line a shell would read back.
shown() {
	local out
	out=$(printf '%q ' "$@")
	printf '%s' "${out% }"
}

# run CMD…: run CMD; with --dry-run, only print it.
run() {
	if ((dry_run)); then
		printf '+ %s\n' "$(shown "$@")"
	else
		"$@"
	fi
}

# exists CMD…: whether CMD (a describe) finds the thing. With --dry-run, print it and say no, so
# every step that makes something is printed too.
exists() {
	if ((dry_run)); then
		printf '? %s\n' "$(shown "$@")"
		return 1
	fi
	"$@" >/dev/null 2>&1
}

# retry CMD…: run CMD up to six times, ten seconds apart. Something just made in Google Cloud
# (a service account) can take a little while to be seen by other services.
retry() {
	local i
	for i in 1 2 3 4 5 6; do
		run "$@" && return 0
		((dry_run)) && return 0
		((i < 6)) && { say "not yet; trying again in 10 seconds"; sleep 10; }
	done
	return 1
}

# setup: read the project from the key file, and sign gcloud in as its service account.
setup() {
	if [[ -f $key_file ]]; then
		command -v jq >/dev/null || die "jq is needed to read $key_file"
		project=$(jq -r '.project_id // empty' "$key_file")
		[[ -n $project ]] || die "$key_file has no project_id: it is not a service account key file"
	elif ((dry_run)); then
		project=PROJECT
		say "($key_file is not there: PROJECT stands in for its project)"
	else
		die "no service account key at $key_file (or AGENTS_GCP_KEY_FILE): see docs/how-to/run-the-control-plane-in-the-cloud.md"
	fi
	if ! ((dry_run)); then
		command -v gcloud >/dev/null || die "gcloud is needed: brew install --cask gcloud-cli"
	fi
	vm_account=$vm_account_name@$project.iam.gserviceaccount.com
	export CLOUDSDK_CONFIG=$HOME/.agents/gcloud/agents-control
	export CLOUDSDK_CORE_PROJECT=$project
	export CLOUDSDK_CORE_DISABLE_PROMPTS=1
	((dry_run)) || mkdir -p "$CLOUDSDK_CONFIG"
	say "CLOUDSDK_CONFIG=$CLOUDSDK_CONFIG CLOUDSDK_CORE_PROJECT=$project"
	run gcloud auth activate-service-account --key-file "$key_file" --quiet
}

# vm_ssh COMMAND: run COMMAND on the VM, over ssh as gcloud sets it up (a key of its own in
# ~/.ssh/google_compute_engine). Standard input goes to COMMAND. ssh asks the VM for a sign of
# life, so a connection that is gone (Wi-Fi that dropped) ends with 255 in 3 minutes: without
# it, ssh waits for ever on a command that says nothing (#7030).
vm_ssh() {
	run gcloud compute ssh "$name" --zone "$zone" --quiet \
		--ssh-flag=-oConnectTimeout=60 --ssh-flag=-oServerAliveInterval=15 --ssh-flag=-oServerAliveCountMax=12 \
		--command "$1"
}

# pick_version: the version up.sh runs, in $version, and its image tag, in $tag: AGENTS_VERSION,
# or scripts/version.sh. Without --build it must be a release's, X.Y.Z, whose image and Linux
# host up.sh pulls; move.sh asks before it stops anything. A tag has no "+" (Docker's rule).
pick_version() {
	version=${AGENTS_VERSION:-$("$(dirname "${BASH_SOURCE[0]}")/../../scripts/version.sh")}
	tag=${version//+/-}
	((build)) || [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
		die "no release is $version, so there is no image to pull: give --build to build this checkout, or AGENTS_VERSION=X.Y.Z for a release"
}

# owner_of_url URL: the owner in a GitHub remote's URL (https://github.com/O/R(.git),
# git@github.com:O/R(.git) or ssh://git@github.com/O/R), in lower case as ghcr.io names it;
# nothing when URL is not GitHub's.
owner_of_url() {
	local url=$1 owner=''
	if [[ $url =~ ^(https://|ssh://git@|git@)github\.com[:/]([A-Za-z0-9-]+)/[^/]+$ ]]; then
		owner=${BASH_REMATCH[2]}
	fi
	printf '%s' "$owner" | tr '[:upper:]' '[:lower:]'
}

# image_of_repo ROOT: set image to the one a release of the repo at ROOT pushes,
# ghcr.io/<owner>/agents-control (#5472), as release.yml names it with github.repository_owner.
# The owner is AGENTS_IMAGE_OWNER when set; else the one GitHub gives for the repo (gh follows a
# moved repo's redirect, so an old origin URL still gives the new owner); else the owner in
# origin's URL. When none says, it stops here, before anything is made or pulled.
image_of_repo() {
	local root=$1 owner=${AGENTS_IMAGE_OWNER:-}
	if [[ -z $owner ]] && command -v gh >/dev/null; then
		owner=$(cd "$root" && gh repo view --json owner -q .owner.login 2>/dev/null) || owner=''
	fi
	if [[ -z $owner ]]; then
		owner=$(owner_of_url "$(git -C "$root" remote get-url origin 2>/dev/null || true)")
	fi
	owner=$(printf '%s' "$owner" | tr '[:upper:]' '[:lower:]')
	[[ $owner =~ ^[a-z0-9-]+$ ]] ||
		die "cannot tell the image's owner: $root has no GitHub origin and gh does not say; give AGENTS_IMAGE_OWNER=<the owner release.yml pushes to>"
	image=ghcr.io/$owner/agents-control
}
