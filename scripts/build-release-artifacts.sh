#!/usr/bin/env bash
set -euo pipefail

version=${1:?usage: bash scripts/build-release-artifacts.sh vX.Y.Z}
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	printf 'invalid release version: %s\n' "$version" >&2
	exit 2
fi

root=$(git rev-parse --show-toplevel)
cd "$root"
if [[ -n $(git status --porcelain --untracked-files=all) ]]; then
	printf 'release artifacts must be built from a clean commit\n' >&2
	exit 1
fi
commit=$(git rev-parse HEAD)
out="$root/dist"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
	IFS=/ read -r goos goarch <<<"$target"
	stage="$tmp/$goos-$goarch"
	mkdir -p "$stage"
	binary=ara-mcp
	if [[ $goos == windows ]]; then
		binary+=.exe
	fi

	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
		-trimpath -ldflags="-s -w -X main.version=$version -X main.commit=$commit" \
		-o "$stage/$binary" ./cmd/ara-mcp
	cp README.md LICENSE THIRD_PARTY_NOTICES.md "$stage/"
	printf 'Source commit: %s\nSource URL: https://github.com/cavenine/ara-mcp/tree/%s\n' \
		"$commit" "$commit" >"$stage/SOURCE.txt"
	archive="$out/ara-mcp_${version}_${goos}_${goarch}.tar.gz"
	tar -czf "$archive" -C "$stage" .
done

(
	cd "$out"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "ara-mcp_${version}_"*.tar.gz >"SHA256SUMS_${version}.txt"
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "ara-mcp_${version}_"*.tar.gz >"SHA256SUMS_${version}.txt"
	else
		printf 'sha256sum or shasum is required to write artifact checksums\n' >&2
		exit 1
	fi
)

printf 'Built archives and checksums in %s\n' "$out"
