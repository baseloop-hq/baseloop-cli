---
name: release-cli
description: Cut, verify, and promote a Baseloop CLI release end to end - preflight on main, pick the version, tag a release candidate, watch the GitHub release workflow, verify the published assets and installer, then promote to a final release. Use this whenever the user wants to release, ship, publish, tag, or version the CLI, cut or promote an rc, check whether a release is good, or asks "is this ready to release" or "what's the next version", even if they don't say the word release. Do not tag by hand with git or scripts/release.sh without going through this flow.
---

# Release the Baseloop CLI

A release is a tag push. `scripts/release.sh VERSION` checks the tree is clean, runs `make release-check`, builds, tags `vVERSION`, and pushes; GitHub Actions (`.github/workflows/release.yml`) then builds every platform, stamps the installer scripts with the version, and publishes the release. Tags are permanent and public, and the installer plus `baseloop upgrade` follow GitHub's "latest" release automatically, so a bad final release reaches every user within a day. The whole point of this flow is to catch problems on a release candidate, which "latest" ignores, before anything is promoted.

Everything below that pushes a tag is irreversible. Confirm with the user before each push, and never tag from a branch other than `main`, retag an existing version, delete or move a tag, or force-push.

## 1. Preflight

`scripts/release.sh` checks for a dirty tree but not for the branch, so do that here:

```bash
git fetch origin main
git rev-parse --abbrev-ref HEAD          # must be main
git status --short                       # must be empty
git rev-list --left-right --count origin/main...HEAD   # must be 0 0
gh auth status
```

If HEAD is not exactly `origin/main`, stop. Releasing unmerged or unpushed commits produces a release nobody can reproduce from the repo.

## 2. Pick the version

```bash
git tag --sort=-v:refname | head -1      # last release
git log $(git tag --sort=-v:refname | head -1)..HEAD --oneline
```

Read the commits since the last tag and propose a bump: patch for fixes only, minor when there is new user-facing behavior (new commands, flags, installer steps, env vars), major only for a deliberate compatibility break. Show the user the commit list and the proposed version and let them confirm. Versions are semver without a `v` prefix in every script (`0.11.0`); the tag gets the `v`.

## 3. Release candidate first

Tag an rc when anything touched the release surface: `scripts/`, `.github/workflows/`, `internal/cli/upgrade.go`, `internal/cli/receipt.go`, or the install docs. Those paths only get exercised by a real published release, and an rc is the only way to run them without moving "latest". For a small code-only patch, going straight to final is acceptable if the user agrees.

```bash
scripts/release.sh 0.11.0-rc.1
```

The hyphen is what makes it a prerelease: the workflow sets `prerelease: true` for hyphenated tags, so "latest" stays on the previous release and nobody is upgraded onto the rc. Bump the rc number (`-rc.2`) for another attempt; never reuse a tag.

## 4. Verify what was published

```bash
scripts/verify-release.sh 0.11.0-rc.1
```

The script waits for the workflow run, then checks: the run succeeded, the release exists and is not a draft, the prerelease flag matches the tag, "latest" points where it should, all nine assets are present (six archives, `checksums.txt`, `install-cli`, `install-cli.ps1`), both installer scripts are stamped with this exact version, this platform's archive matches `checksums.txt`, and the published `install-cli` installs a binary that reports this version into a throwaway home with a `managed` receipt. It exits non-zero on any failure and lists them at the end.

When it fails, read the failure line before reacting:

| Failure | Usual meaning | What to do |
| --- | --- | --- |
| no release workflow run found | tag was not pushed, or pushed to a fork | check `git ls-remote --tags origin` |
| workflow conclusion=failure | build or asset step broke | `gh run view <id> --log-failed`; fix on main, tag the next rc |
| missing asset install-cli | `gen-installer-assets.sh` did not run or found the placeholder zero or twice | the workflow log says which; the installer scripts must contain `PINNED_DEFAULT_VERSION=""` exactly once |
| install-cli is not pinned | stamp pattern drifted in `scripts/install.sh` | fix the placeholder line, next rc |
| 'latest' points at the rc | prerelease flag lost | check `release.yml`'s `prerelease:` expression |
| installed binary reports a different version | ldflags version injection broke in `build-release.sh` | fix, next rc |
| published installer failed | real user-facing breakage | read the log the script prints; this is exactly what the rc exists to catch |

Fix on `main` through a normal PR, then tag the next rc. Do not patch a release by hand or re-upload assets.

## 5. Promote to final

Only after the rc verifies clean, and from the same commit:

```bash
git rev-parse HEAD            # must equal the rc's commit: git rev-list -n1 v0.11.0-rc.1
scripts/release.sh 0.11.0
scripts/verify-release.sh 0.11.0
```

The final verification additionally requires "latest" to have moved to the new tag, because that is the moment the installer and `baseloop upgrade` start handing it to users.

## 6. After the release

- **App install route.** `app.baseloop.io/install-cli` and `/install-cli.ps1` must redirect to the latest release's `install-cli` assets, not to raw GitHub `main` (DISTRIBUTION.md, "App Route Contract"). Check where they point now:
  ```bash
  curl -sI https://app.baseloop.io/install-cli | grep -i '^location'
  ```
  If it still resolves to `raw.githubusercontent.com/.../main/...`, the pinning this pipeline produces is not reaching users yet. Tell the user; the fix lives in the app repo, not here.
- Mention anything an operator should know from the commit list: new env vars, new installer prompts, changed update behavior.

## Report

End with a short summary in this shape:

```
Released v0.11.0 (from rc.1)
  commit:   <sha>
  release:  <url>
  verified: 20 checks passed
  latest:   moved from v0.10.0 to v0.11.0
  follow-up: app install route still points at main (owner: app repo)
```
