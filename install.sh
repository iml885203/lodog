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
else
  base="https://github.com/$repo/releases/latest/download"
fi
asset="lodog_${os}_${arch}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

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
