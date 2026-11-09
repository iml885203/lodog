#!/bin/sh
# Install the latest lodog release.
#   curl -fsSL https://raw.githubusercontent.com/iml885203/lodog/main/install.sh | sh
# Env: LODOG_VERSION (tag, default latest), LODOG_INSTALL_DIR (default ~/.local/bin).
set -eu

repo="iml885203/lodog"
dir="${LODOG_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "lodog: unsupported OS $(uname -s); use macOS or Linux" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "lodog: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${LODOG_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$LODOG_VERSION"
  api="https://api.github.com/repos/$repo/releases/tags/$LODOG_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
  api="https://api.github.com/repos/$repo/releases/latest"
fi
asset="lodog_${os}_${arch}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# github.com/<repo>/releases/download/ sometimes answers 504 for minutes while
# the same asset still downloads through the REST API, so fall back to it.
fetch() {
  curl -fsSL --retry 2 --retry-delay 3 -o "$tmp/$1" "$base/$1" 2>/dev/null && return 0
  [ -f "$tmp/release.json" ] || curl -fsSL --retry 3 --retry-delay 5 -o "$tmp/release.json" "$api"
  # The release JSON lists each asset's API url line before its name line.
  url="$(awk -v n="\"name\": \"$1\"" '/"url": ".*\/releases\/assets\// { u = $2 } index($0, n) { gsub(/[",]/, "", u); print u; exit }' "$tmp/release.json")"
  [ -n "$url" ] || { echo "lodog: $1 not found in the release" >&2; exit 1; }
  curl -fsSL --retry 3 --retry-delay 5 -H 'Accept: application/octet-stream' -o "$tmp/$1" "$url"
}
fetch "$asset"
fetch checksums.txt

want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
  got="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "lodog: checksum mismatch for $asset" >&2
  exit 1
fi

mkdir -p "$dir"
install -m 0755 "$tmp/$asset" "$dir/lodog"
echo "installed $("$dir/lodog" --version) to $dir/lodog"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "add $dir to your PATH, e.g.: export PATH=\"$dir:\$PATH\"" ;;
esac
