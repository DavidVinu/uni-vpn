#!/usr/bin/env bash
# Puts FILE... on a GitHub release so that its download links keep working while they upload.
# "gh release upload --clobber" deletes the old file first: its releases/latest/download/ link
# answers 404 for the whole upload, and for good when the upload fails. Here every file goes
# up under a temporary name first (retried), and only then, in the order given, the old file is
# deleted and the new one takes its name: a link is missing only between those two calls, and a
# failed upload leaves the old file in place.
# Usage: packaging/release-put.sh TAG FILE...  (CI: GH_TOKEN and GITHUB_REPOSITORY set)
set -euo pipefail
tag="$1"
shift
repo="${GITHUB_REPOSITORY:?}"
api="repos/$repo/releases"

# retry COMMAND...: four tries with a pause in between, for GitHub's occasional 5xx or timeout.
# Only the successful try's output counts: on an error gh api prints GitHub's error body to
# stdout, which must not end up in a caller's $(...).
retry() {
  local n out
  for n in 1 2 3 4; do
    if out=$("$@"); then
      if [ -n "$out" ]; then printf '%s\n' "$out"; fi
      return 0
    fi
    if [ "$n" = 4 ]; then return 1; fi
    echo "failed ($*), trying again" >&2
    sleep $((n * ${RELEASE_PUT_PAUSE:-10}))
  done
}

# gone ID: the asset no longer exists (after a DELETE whose answer got lost).
gone() { ! gh api "$api/assets/$1" --silent 2>/dev/null; }
delete() { gh api -X DELETE "$api/assets/$1" --silent || gone "$1"; }
rename() { gh api -X PATCH "$api/assets/$1" -f name="$2" --silent; }

release=$(retry gh api "$api/tags/$tag" --jq .id)
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
for file in "$@"; do
  cp "$file" "$staging/next-${file##*/}"
  # --clobber here only replaces what an earlier, failed run left under the temporary name.
  retry gh release upload "$tag" "$staging/next-${file##*/}" --clobber --repo "$repo"
done
for file in "$@"; do
  name=${file##*/}
  assets=$(retry gh api "$api/$release/assets?per_page=100" --paginate --jq '.[] | "\(.id) \(.name)"')
  new=$(awk -v n="next-$name" '$2 == n { print $1 }' <<<"$assets")
  old=$(awk -v n="$name" '$2 == n { print $1 }' <<<"$assets")
  if [ -z "$new" ]; then
    echo "next-$name is not on the release" >&2
    exit 1
  fi
  if [ -n "$old" ]; then retry delete "$old"; fi
  retry rename "$new" "$name"
  echo "$name replaced"
done
