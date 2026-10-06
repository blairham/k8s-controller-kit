---
description: Cut a v0.0.x release tag (the tag is the release; the release workflow publishes signed evidence beside it).
argument-hint: "<version, e.g. v0.0.1>"
allowed-tools: Bash(make:*), Bash(go test:*), Bash(go vet:*), Bash(go build:*), Read, Edit, Glob, Grep
---

Cut a k8s-controller-kit release. A consumer picks up the new version via a
`go get github.com/blairham/k8s-controller-kit@$1` tag bump, so the release IS the git
tag. Pushing it runs `.github/workflows/release.yml`, which publishes the
GitHub release (source archive, signed checksums, provenance) with the tag's
CHANGELOG section as the notes — and fails if that section is missing. Target version: `$1` (must be `v0.0.x`, pre-stable).

Steps:
1. Confirm the working tree is clean and we're on `main` (or the branch the user
   intends). If dirty, stop and report.
2. Check CI on the commit you will tag (`gh run list --branch main -L 3`) — the
   tag must point at a green commit. Do not run `make check` locally; CI ran
   it. If CI is red, stop.
3. Verify `$1` is a valid, monotonically-increasing `v0.0.x` tag — check
   `git tag --list 'v0.0.*'` and `git describe --tags --abbrev=0` so we don't
   reuse or skip backwards.
4. Update `CHANGELOG.md`: move the Unreleased entries under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading (no `v` — the release workflow extracts the
   notes by that exact shape), leave a fresh Unreleased section, and add the
   compare link at the bottom if the file keeps them.
5. STOP and show the user the proposed CHANGELOG diff and the exact commands
   before running anything that mutates the remote. `main` is protected, so
   the CHANGELOG move lands as a `Release $1` PR (squash-merged once CI is
   green); then `git tag -s $1 -m "$1: <summary>" <merge commit>` and
   `git push origin $1`. Do not push tags without explicit confirmation.
6. Watch the Release workflow, and check the release carries
   `k8s-controller-kit-X.Y.Z.tar.gz`, `checksums.txt`, `checksums.txt.sigstore.json` and
   `k8s-controller-kit-$1.intoto.jsonl`.
