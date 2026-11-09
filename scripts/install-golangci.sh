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
# github.com/<repo>/releases/download/ answers 504 for some assets for minutes
# at a time, while the same asset still downloads through the REST API. Try the
# plain URL first, then the API, rather than fail the build with no code change.
# The checksum below is verified either way.
api="https://api.github.com/repos/golangci/golangci-lint/releases"
fetch() {
	local name="$1" out="$2" url
	curl -sSfL --retry 2 --retry-delay 3 --retry-all-errors "${base}/${name}" -o "$out" && return 0
	echo "install-golangci: ${name} download failed, trying the GitHub API" >&2
	# Saved to a file, not piped: awk exits at the match, and under pipefail the
	# curl it cut off would fail the step.
	[ -f "${work}/release.json" ] ||
		curl -sSfL --retry 3 --retry-delay 5 --retry-all-errors "${api}/tags/${version}" -o "${work}/release.json"
	# The release JSON lists each asset's API url line before its name line.
	url="$(awk -v n="\"name\": \"${name}\"" '/"url": ".*\/releases\/assets\// { u = $2 } index($0, n) { gsub(/[",]/, "", u); print u; exit }' "${work}/release.json")"
	[ -n "$url" ] || { echo "install-golangci: ${name} not in release ${version}" >&2; return 1; }
	curl -sSfL --retry 3 --retry-delay 5 --retry-all-errors -H 'Accept: application/octet-stream' "$url" -o "$out"
}
fetch "${archive}" "${work}/${archive}"
fetch "golangci-lint-${bare}-checksums.txt" "${work}/checksums.txt"

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
