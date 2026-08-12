# Releasing AgentMux

AgentMux releases are built by GoReleaser when a semantic version tag is
pushed. Each platform archive contains the complete Web Console, `amux`, and
the companion `agentmux-hook` binary.

## One-time GitHub setup

1. Create a public repository named `homebrew-tap` under the
   `wangning19940904` GitHub account. Its default branch must be `main`.
2. Create a fine-grained personal access token that can write repository
   contents in `homebrew-tap`.
3. Add that token to the AgentMux repository as an Actions secret named
   `HOMEBREW_TAP_GITHUB_TOKEN`.
4. Choose a project license and add it as `LICENSE` before a public release.

The Homebrew secret is optional. Without it, GitHub Releases still publish and
GoReleaser generates the Cask locally, but it does not push to the Tap.

## Validate a release locally

Install GoReleaser v2, then run:

```bash
goreleaser check
HOMEBREW_TAP_GITHUB_TOKEN='' goreleaser release --snapshot --clean --skip=publish
```

Inspect `dist/artifacts.json`, the archives, and the generated Cask. Verify an
archive contains both binaries:

```bash
tar -tzf dist/agentmux_darwin_arm64.tar.gz
```

## Publish

Ensure the branch is clean and tests pass, then create and push an annotated
semantic version tag:

```bash
go test ./...
git tag -a v0.1.0 -m "AgentMux v0.1.0"
git push origin v0.1.0
```

The Release workflow builds macOS and Linux archives for amd64/arm64, plus a
Windows amd64 zip, writes `checksums.txt`, publishes the GitHub Release, and
updates `wangning19940904/homebrew-tap` for non-prerelease versions.

To publish a prerelease, use a tag such as `v0.2.0-rc.1`. It appears as a
GitHub prerelease and does not update Homebrew.

## Installation checks

After the workflow succeeds, verify all supported paths:

```bash
brew update
brew install wangning19940904/tap/agentmux
amux version

curl -fsSL https://raw.githubusercontent.com/wangning19940904/AgentMux/main/install.sh | sh
~/.local/bin/amux version

go install github.com/wangning19940904/AgentMux/cmd/amux@latest
```

The `go install` build is the lightweight CLI build and does not contain the
React Web Console or the companion hook binary. GitHub Releases, the installer,
and Homebrew are the supported full installations.
