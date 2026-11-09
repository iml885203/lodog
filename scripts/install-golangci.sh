#!/usr/bin/env bash
# Installs the pinned golangci-lint into .tools/ so the linters run without
# Docker.
#
# A pinned release tarball with its checksum verified, not `go install` and not
# a piped install script. `go install` resolves 30-odd analyser modules through
# sum.golang.org, where a transient error fails the build with no code change;
# a piped script is unpinned code executing with the developer's privileges,
# which is the supply-chain shape this repository avoids elsewhere.
set -euo pipefail

version="${1:?usage: install-golangci.sh <version> <dest-dir>}"
dest="${2:?usage: install-golangci.sh <version> <dest-dir>}"
bare="${version#v}"

case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) echo "install-golangci: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) echo "install-golangci: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

# linux-arm64 has no published tarball for this release; Docker still covers it.
archive="golangci-lint-${bare}-${os}-${arch}.tar.gz"
base="https://github.com/golangci/golangci-lint/releases/download/${version}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "install-golangci: fetching ${archive}"
curl -sSfL "${base}/${archive}" -o "${work}/${archive}"
curl -sSfL "${base}/golangci-lint-${bare}-checksums.txt" -o "${work}/checksums.txt"

# Verify before unpacking. An archive that does not match its published
# checksum is not unpacked at all, let alone executed.
(
	cd "$work"
	grep " ${archive}\$" checksums.txt > expected.txt
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c expected.txt
	else
		shasum -a 256 -c expected.txt
	fi
)

tar -xzf "${work}/${archive}" -C "$work"
mkdir -p "$dest"
install -m 0755 "${work}/golangci-lint-${bare}-${os}-${arch}/golangci-lint" "${dest}/golangci-lint"

echo "install-golangci: $("${dest}/golangci-lint" --version)"
