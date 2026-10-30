#!/usr/bin/env bash
# publish-release.sh — publish the current commit as the "continuous" release.
#
# One rolling prerelease, rebuilt at every commit, with stable asset names:
#
#   https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-amd64.tar.gz
#   https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-arm64.tar.gz
#   https://github.com/buzzqw/indexarr/releases/download/continuous/SHA256SUMS
#
# The tag "continuous" is moved to the new commit and the assets are replaced;
# the versioned tags (v2.1.3, ...) and the "latest" release are left alone.
#
#   scripts/publish-release.sh              # dry run: builds, uploads nothing
#   scripts/publish-release.sh --yes        # build, move the tag, publish
#   scripts/publish-release.sh --auto --yes # what the post-commit hook runs
#   scripts/publish-release.sh --install-hook   # publish after every commit
#
# The hook is opt-in and local: it runs in the background, logs to
# ci-logs/publish.log, skips rebases and is disabled with
# INDEXARR_NO_PUBLISH=1. GitHub Actions stays off.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TAG="continuous"
TITLE="Continuous build (linux amd64 + arm64)"
YES=0
AUTO=0
ALLOW_DIRTY=0
INSTALL_HOOK=0

usage() {
	cat <<'TXT'
Usage: scripts/publish-release.sh [options]

Options:
  --tag NAME        Release tag to update (default: continuous)
  --yes             Execute; without it nothing is pushed or uploaded
  --auto            Hook mode: no prompts, quiet, skipped during a rebase
  --allow-dirty     Publish even with uncommitted tracked changes
  --install-hook    Install .git/hooks/post-commit and exit
  -h, --help        This help

Environment:
  INDEXARR_NO_PUBLISH=1   Make the post-commit hook a no-op
  GH_REPO=owner/name      Repository to publish to (default: origin)
TXT
}

die() {
	echo "error: $*" >&2
	exit 1
}

note() {
	[[ "$AUTO" == 1 ]] || echo "$@"
}

while (($#)); do
	case "$1" in
	--tag)
		[[ $# -ge 2 ]] || die "--tag requires a value"
		TAG="$2"
		shift 2
		;;
	--yes)
		YES=1
		shift
		;;
	--auto)
		AUTO=1
		shift
		;;
	--allow-dirty)
		ALLOW_DIRTY=1
		shift
		;;
	--install-hook)
		INSTALL_HOOK=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown option: $1 (use --help)"
		;;
	esac
done

install_hook() {
	local hooks hook
	hooks="$(git rev-parse --git-path hooks)"
	mkdir -p "$hooks"
	hook="${hooks}/post-commit"
	if [[ -f "$hook" ]] && ! grep -q "publish-release.sh" "$hook"; then
		die "${hook} already exists and is not ours; move it away first"
	fi
	cat >"$hook" <<'SH'
#!/usr/bin/env bash
# Installed by scripts/publish-release.sh --install-hook
# Publishes the "continuous" release in the background after every commit.
set -u
[ "${INDEXARR_NO_PUBLISH:-0}" = "1" ] && exit 0
root="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
[ -x "${root}/scripts/publish-release.sh" ] || exit 0
# Rebases and cherry-picks create commits we do not want to publish one by one.
[ -d "${root}/.git/rebase-merge" ] && exit 0
[ -d "${root}/.git/rebase-apply" ] && exit 0
mkdir -p "${root}/ci-logs"
nohup setsid "${root}/scripts/publish-release.sh" --auto --yes \
	>>"${root}/ci-logs/publish.log" 2>&1 </dev/null &
exit 0
SH
	chmod 0755 "$hook"
	echo "Hook installed: ${hook}"
	echo "Disable it with INDEXARR_NO_PUBLISH=1 or remove the file."
}

if [[ "$INSTALL_HOOK" == 1 ]]; then
	install_hook
	exit 0
fi

# One publisher at a time: two builds of the same assets would race on the
# upload. flock is optional (macOS lacks it), the lock is then best-effort.
mkdir -p "${ROOT}/ci-logs"
if command -v flock >/dev/null 2>&1; then
	exec 9>"${ROOT}/ci-logs/.publish.lock"
	if ! flock -n 9; then
		note "another publish is running: skipped"
		exit 0
	fi
fi

command -v gh >/dev/null 2>&1 || die "gh is not installed (https://cli.github.com)"
gh auth status >/dev/null 2>&1 || die "gh is not logged in: run 'gh auth login'"

if [[ -z "${GH_REPO:-}" ]]; then
	remote="$(git remote get-url origin 2>/dev/null)" || die "no origin remote"
	GH_REPO="$(sed -E 's#^(git@|https://)github.com[:/]##; s#\.git$##' <<<"$remote")"
fi
[[ "$GH_REPO" =~ ^[^/]+/[^/]+$ ]] || die "cannot work out the repository from origin; set GH_REPO=owner/name"

git rev-parse --verify HEAD >/dev/null 2>&1 || die "no commit to publish"
SHA="$(git rev-parse --short HEAD)"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

if [[ "$ALLOW_DIRTY" == 0 && -n "$(git status --porcelain --untracked-files=no)" ]]; then
	die "uncommitted changes: commit them, or pass --allow-dirty"
fi

note "Publishing ${VERSION} (${SHA}) to ${GH_REPO} as ${TAG}."

"${ROOT}/scripts/package-release.sh" --out "${ROOT}/dist" --version "$VERSION"

ASSETS=()
for f in "${ROOT}"/dist/indexarr-*.tar.gz "${ROOT}/dist/SHA256SUMS"; do
	[[ -f "$f" ]] && ASSETS+=("$f")
done
[[ ${#ASSETS[@]} -ge 3 ]] || die "the archives are missing: ${ROOT}/dist"

BODY="$(cat <<MD
Rolling build of \`main\`, refreshed at every commit. Not a versioned release:
use it to try the current code, not as a fixed point in production.

- version: \`${VERSION}\`
- commit: \`${SHA}\` — $(git log -1 --format=%s)
- built: $(date -u +%Y-%m-%dT%H:%M:%SZ)

Each archive is self-contained (binary, installer, unit, examples):

\`\`\`bash
tar xzf indexarr-linux-amd64.tar.gz
cd indexarr-${VERSION}-linux-amd64
./install.sh
\`\`\`

Verify with \`sha256sum -c SHA256SUMS\`.
MD
)"

if [[ "$YES" == 0 ]]; then
	note "Dry run: would move tag ${TAG} to ${SHA} and upload:"
	for f in "${ASSETS[@]}"; do
		note "  $(basename "$f")"
	done
	note "Run again with --yes to publish."
	exit 0
fi

# Move the rolling tag. A lightweight tag is enough: the release page shows the
# commit and the body records version and short hash.
git tag -f "$TAG" HEAD >/dev/null
git push --force origin "refs/tags/${TAG}" >/dev/null

if gh release view "$TAG" --repo "$GH_REPO" >/dev/null 2>&1; then
	gh release upload "$TAG" "${ASSETS[@]}" --repo "$GH_REPO" --clobber >/dev/null
	gh api -X PATCH "repos/${GH_REPO}/releases/tags/${TAG}" \
		-f body="$BODY" -F prerelease=true -F make_latest=false >/dev/null
	note "Updated ${TAG}: ${SHA}"
else
	gh release create "$TAG" "${ASSETS[@]}" \
		--repo "$GH_REPO" \
		--title "$TITLE" \
		--notes "$BODY" \
		--prerelease --latest=false >/dev/null
	note "Created ${TAG}: ${SHA}"
fi

note
note "Assets:"
for arch in amd64 arm64; do
	note "  https://github.com/${GH_REPO}/releases/download/${TAG}/indexarr-linux-${arch}.tar.gz"
done
note "  https://github.com/${GH_REPO}/releases/download/${TAG}/SHA256SUMS"
