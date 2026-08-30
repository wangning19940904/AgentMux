# Releasing AgentMux

AgentMux releases are built by GoReleaser when a semantic version tag is
pushed. Each platform archive contains the complete Web Console, `amux`, and
the companion `agentmux-hook` binary. The same tag also versions and publishes
the Python SDK (`agentmux-sdk` on PyPI) and packs the TypeScript SDK
(`agentmux-sdk` on npm), so all three artifacts share one version number.

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

## One-time PyPI setup (Trusted Publishing)

The `publish-python-sdk` job authenticates with PyPI via OIDC; no API token
secret is stored in GitHub.

1. Register at <https://pypi.org/account/register/> and enable 2FA (mandatory).
2. Open <https://pypi.org/manage/account/publishing/> → "Add a new pending
   publisher" → GitHub, and fill in: project `agentmux-sdk`, owner
   `wangning19940904`, repository `AgentMux`, workflow `release.yml`,
   environment `pypi`.
3. In the AgentMux repository: Settings → Environments → New environment named
   `pypi` (optionally restrict it to tag deployments).
4. Nothing else. The first successful publish converts the pending publisher
   into the project's trusted publisher.

Alternative for a manual first release: create an account-scoped API token,
run `cd sdk/python && uv build && uv publish --token pypi-...`, then replace
it with a project-scoped token (or set up Trusted Publishing afterwards).

## One-time npm setup (Trusted Publishing)

The `publish-typescript-sdk` job authenticates with npm via OIDC; no API token
secret is stored in GitHub. The npm package name is the unscoped `agentmux-sdk`
(the `agentmux` org name is taken).

1. Open <https://www.npmjs.com/package/agentmux-sdk/access> → "Trusted
   Publisher" → GitHub Actions, and fill in: organization `wangning19940904`,
   repository `AgentMux`, workflow filename `release.yml` (no environment),
   with "Allow npm publish" checked.
2. Nothing else. The job grants `id-token: write` and upgrades npm to >= 11.5.1
   (required for OIDC), then `npm publish` exchanges the id-token for
   short-lived credentials automatically.

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
(cd sdk/python && uv run pytest -q)
(cd sdk/typescript && npm test)
git tag -a vX.Y.Z -m "AgentMux vX.Y.Z"
git push origin vX.Y.Z
```

The Release workflow builds macOS and Linux CLI archives for amd64/arm64, plus
a Windows amd64 zip and the native macOS desktop app. It writes checksums for
the CLI and desktop artifacts, uploads
`scripts/ensure-agentmux.sh` as a release asset, publishes the GitHub Release,
updates `wangning19940904/homebrew-tap` for non-prerelease versions, publishes
`agentmux-sdk` to PyPI (Trusted Publishing) and publishes the npm
`agentmux-sdk` package (also Trusted Publishing).

## Contract change checklist

Before tagging a release that touched the public API surface:

1. `go test ./contract/` must pass. If it reports drift and the change is
   intentional, regenerate goldens with
   `go test ./contract/ -run TestContractGolden -update`.
2. Update `contract/openapi.yaml` and the SDK models
   (`sdk/python/src/agentmux_sdk/models.py`, `sdk/typescript/src/types.ts`).
3. Decide whether `contract.Version` (in `contract/contract.go`) needs a
   minor (compatible addition) or major (breaking) bump, and record the
   change in `contract/CONTRACT.md`.
4. SDK-side alignment tests (`sdk/python/tests/test_contract_alignment.py`,
   `sdk/typescript/tests/contract-alignment.test.ts`) must pass.

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
