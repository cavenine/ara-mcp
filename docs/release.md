# Build and release artifacts

No ara-mcp release has been published. The planned first version is `v0.1.0`; it
is not a tag or downloadable artifact. Publish only after reviewing the compatibility
and operation evidence linked in [API contracts](api-contracts.md#remaining-release-and-implementation-gates)
and the [first-release policy](first-release-policy.md#outstanding-verification).
Creating a tag or GitHub release is a separate action.

## Build from source

Use Go 1.27.x. For a local MCP client, build on the agent computer:

```sh
git clone https://github.com/cavenine/ara-mcp.git
cd ara-mcp
mkdir -p bin
go build -trimpath -ldflags='-s -w' -o ./bin/ara-mcp ./cmd/ara-mcp
./bin/ara-mcp version
```

Use the absolute `bin/ara-mcp` path in the client's stdio MCP configuration. For
the observatory service, cross-build Linux ARM64 and copy the executable as described
in [persistent deployment](deployment.md#install). Local and service MCP configuration
examples are in the end-user [README](../README.md#choose-how-to-run-it).

## Prepare binary archives

Run from the exact clean source commit selected for a release, after its checks pass:

```sh
bash scripts/build-release-artifacts.sh v0.1.0
```

The script builds a static executable and `.tar.gz` archive for each target already
cross-built in CI: Linux amd64/arm64, macOS amd64/arm64, and Windows amd64. Each
archive contains the executable, README, AGPL license, third-party notice index, and
the source commit/URL. It does not include credentials, local config, generated
profiles, or source code. The script writes `dist/SHA256SUMS_v0.1.0.txt`; `dist/` is
git-ignored. Inspect every archive and checksum file before attaching them to a
release.

The target list describes successful Go builds, not runtime validation. Linux ARM64
was exercised on a Raspberry Pi 4 with Debian 13 and OmniSim. Linux amd64, macOS,
and Windows are CI cross-builds only; no hardware, operating-system runtime, or
named MCP client support is implied.

Verify downloads before extraction:

```sh
# Linux
cd dist
sha256sum -c SHA256SUMS_v0.1.0.txt

# macOS
shasum -a 256 -c SHA256SUMS_v0.1.0.txt
```

On Windows, compare each archive's SHA-256 with the matching line using
`Get-FileHash <archive> -Algorithm SHA256`.

## Source, license, and release notes

- Keep the exact source commit public. A release must link its matching repository
  tag/source archive, not just the moving `main` branch. The `SOURCE.txt` in each
  binary archive identifies the source commit.
- The project license is [`LICENSE`](../LICENSE), AGPL-3.0-or-later. The exact
  runtime module versions are in [`go.mod`](../go.mod) and [`go.sum`](../go.sum);
  [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md) links the direct dependency
  sources and their license/notice locations. Keep that notice index in every binary
  archive.
- For an HTTP service, make the corresponding source commit easy for network users
  to find (for example, link the exact commit from the service's public deployment
  information). The project README identifies the repository and license.
- Prepare user-facing release notes from the `Unreleased` entries in
  [`CHANGELOG.md`](../CHANGELOG.md), state the tested Ara commit and deployment
  evidence, and preserve all compatibility limits. Do not claim Pi 3, physical-rig,
  trusted-certificate/reverse-proxy, minimum-memory, or named MCP-host support without
  new evidence.
- Release only from a reviewed commit after `go test -race -shuffle=on ./...`,
  `go vet ./...`, `go build ./...`, `go mod tidy -diff`, the CI cross-build matrix,
  and the applicable live checks pass. Publish checksums and the source link with the
  archives. Do not create or publish a tag as part of ordinary development.

The first release may document only behavior supported by the current pinned Ara
evidence. Outstanding Ara baseline, operation-correlation, palette, and manual-action
limits remain visible in the [API contracts](api-contracts.md#remaining-release-and-implementation-gates)
and [tool reference](tools.md).
