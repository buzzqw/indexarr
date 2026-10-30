#!/usr/bin/env bash
# publish-release.sh — publish the current commit as the "continuous" release.
#
# One rolling prerelease with stable asset names:
#
#   https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-amd64.tar.gz
#   https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-arm64.tar.gz
#   https://github.com/buzzqw/indexarr/releases/download/continuous/SHA256SUMS
#
# The tag "continuous" is moved to the new commit and the assets are replaced;
# the versioned tags (v2.1.3, ...) and the "latest" release are left alone.
#
# Publishing is deliberately manual: no post-commit hook and no GitHub Actions
# workflow. Run it when you decide to refresh the rolling release.
#
#   scripts/publish-release.sh              # dry run: builds, uploads nothing
#   scripts/publish-release.sh --yes        # build, move the tag, publish
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TAG="continuous"
TITLE="Continuous build (linux amd64 + arm64)"
YES=0
ALLOW_DIRTY=0

usage() {
	cat <<'TXT'
Usage: scripts/publish-release.sh [options]

Options:
  --tag NAME        Release tag to update (default: continuous)
  --yes             Execute; without it nothing is pushed or uploaded
  --allow-dirty     Publish even with uncommitted tracked changes
  -h, --help        This help

Environment:
  GH_REPO=owner/name      Repository to publish to (default: origin)
TXT
}

die() {
	echo "error: $*" >&2
	exit 1
}

note() {
	echo "$@"
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
	--allow-dirty)
		ALLOW_DIRTY=1
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
VERSION="$("${ROOT}/scripts/version.sh")"

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
Rolling build of \`main\`, published manually. Not a versioned release: use it
to try the current code, not as a fixed point in production.

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

# GitHub can answer 404 for a moment right after the tag moves: the release
# lookup and the asset upload go through the tag name. Retry with a growing
# pause instead of failing a good build on a race.
retry() {
	local attempt=1 delay=2
	while ! "$@"; do
		if ((attempt >= 5)); then
			return 1
		fi
		note "transient failure, retrying in ${delay}s ..."
		sleep "$delay"
		attempt=$((attempt + 1))
		delay=$((delay * 2))
	done
}

if [[ "$YES" == 0 ]]; then
	note "Dry run: would move tag ${TAG} to ${SHA} and upload:"
	for f in "${ASSETS[@]}"; do
		note "  $(basename "$f")"
	done
	note "Run again with --yes to publish."
	exit 0
fi

# The release id is resolved before the tag moves: after the force push the
# releases/tags/<tag> endpoint can answer 404 for a few seconds.
RELEASE_ID=""
if gh release view "$TAG" --repo "$GH_REPO" >/dev/null 2>&1; then
	RELEASE_ID="$(gh api "repos/${GH_REPO}/releases/tags/${TAG}" --jq .id)"
fi

# Move the rolling tag. A lightweight tag is enough: the release page shows the
# commit and the body records version and short hash.
git tag -f "$TAG" HEAD >/dev/null
git push --force origin "refs/tags/${TAG}" >/dev/null

if [[ -n "$RELEASE_ID" ]]; then
	retry gh release upload "$TAG" "${ASSETS[@]}" --repo "$GH_REPO" --clobber >/dev/null
	retry gh api -X PATCH "repos/${GH_REPO}/releases/${RELEASE_ID}" \
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
