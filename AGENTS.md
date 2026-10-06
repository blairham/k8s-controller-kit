# AGENTS.md — k8s-controller-kit

Guidance for AI coding agents working in this repo. `CLAUDE.md` imports it, and
other tools read this file directly.

## Project Overview

Shared building blocks for blairham's **plan-driven** Kubernetes controllers
(`database-controller`, `kafka-controller`, ...): each reads a target system,
plans the difference from a resource's spec, and either applies it (Enforce)
or records it in status (Observe).

- Module: `github.com/blairham/k8s-controller-kit`
- Go 1.26, `controller-runtime` v0.25, `prometheus/client_golang`
- A library: no binary, no CRDs, no chart.

## Releases and consumers

The Go module is the product: a release *is* a signed git tag, and consumers
pick it up with `go get github.com/blairham/k8s-controller-kit@vX.Y.Z`.
Pushing the tag runs `.github/workflows/release.yml`, which publishes a GitHub
release carrying the tagged source archive, a cosign-signed `checksums.txt`
and SLSA build provenance (also stored as a GitHub attestation). GoReleaser
builds nothing (`builds: skip`); the notes are the tag's `CHANGELOG.md`
section, and the job fails if it is missing. The first tag is `v0.0.0`.
Read `.claude/commands/release-tag.md` before cutting one.

## Repository and CI

Apache-2.0 with a CLA (`CLA.md`); every `.go` file carries the two-line SPDX
header.

- `.github/workflows/ci.yml` -- pre-commit (the lint surface) and build +
  race tests, which also run the fuzz seeds and the `Example` tests. CodeQL
  (`codeql.yml`, `security-extended`) and OpenSSF Scorecard (`scorecard.yml`)
  run alongside. Every action is pinned by commit SHA with a `# vX.Y.Z`
  comment, workflows are read-only by default, and Dependabot bumps the pins
  and the Go modules monthly (the Kubernetes modules as one group).
- Fuzz targets: `plan/plan_fuzz_test.go` (any graph: order, skip rule, an
  invalid graph runs nothing) and `reconciler/inventory_fuzz_test.go` (the
  set arithmetic pruning rests on). Each checks a property, and each has
  been shown to fail against a broken implementation.
- `SECURITY.md` states what the kit does with a controller's credentials and
  what counts as a vulnerability -- notably that pruning trusts the status
  inventory, so status write access must stay with the controller. Keep it
  true when behavior changes.

## Quick Reference

```sh
make test    # unit tests (fake client, no cluster)
make vet
make fmt     # gofumpt
```

## Project Structure

```
plan/          Plan and Step: Describe (dry run), Apply (per-step outcomes), Hash;
               an optional dependency graph (Identified/Dependent) with outputs
               passed forward at apply time (SetOutput/Ref), and Op, a ready-made Step
reconciler/    the generic Enforce/Observe reconciler, Ready/Converged conditions,
               finalizer handling, per-resource metrics, and ownership + pruning
secret/        read a key from a Secret in the resource's own namespace
manager/       the shared main: flags, --controllers, leader election, readyz
```

## What belongs here

Only what is genuinely engine-neutral and already used, or about to be used,
by two controllers. A domain concept (a PostgreSQL role, a Kafka ACL) never
does. A consumer keeps its own API types: `reconciler.Status` points at the
consumer's status fields rather than embedding a struct, so each CRD keeps its
own JSON field names and none of them imports this module.

Behavior here is load-bearing for every consumer. The non-obvious lines exist
because of specific failures (see the comment on `setFinalizer`); keep the test
that pins each, and treat a change to a condition reason, message shape or
metric name as a breaking change for the consumers' alerts.

## Mechanics, compared with aws-load-balancer-controller

The model-then-synthesize shape is the same: build the desired state, diff it
against the target, change only the difference.

- **Dependency graph.** A step that implements `Identified` can be depended on
  by one implementing `Dependent`; `Apply` runs in dependency order, keeping
  insertion order among ready steps, so a plan without dependencies is the
  plain ordered list it always was (same text, same hash). A step publishes a
  value with `SetOutput` and a later one reads it with a `Ref` -- the
  equivalent of the LBC's tokens (a listener reading a target group's ARN).
  Dependents of a failed best-effort step are skipped with a warning. A
  cycle, an unknown dependency or a duplicate id runs nothing.
- **Ownership and pruning.** The LBC tags what it creates and deletes tagged
  resources the model no longer wants. Data-plane objects cannot carry tags,
  so the reconciler keeps an inventory of keys in `Status.Owned`; a Session
  implementing `Pruner` says which keys the spec wants and how to delete the
  rest. Stale keys are pruned in a cleanup phase **after** the main plan
  applies, and leave the inventory only once the target no longer has them.
  A key the spec wants is adopted even if something else created it. Pruning
  is off unless both `Status.Owned` is set and the Session implements
  `Pruner`. See the comment at the top of `reconciler/prune.go`.

## Code Conventions

- Formatting and lint run as **pre-commit hooks**, never by hand (see the
  workspace `AGENTS.md`). golangci-lint v2 is pinned in go.mod's `tool` block
  and in `.pre-commit-config.yaml`; move the two together.
- Every `.go` file starts with the two-line SPDX header
  (`hack/boilerplate.go.txt`); `check-license-headers` enforces it.
- `.tool-versions` and go.mod's `go` directive must match.
- Comments are short and explain why, not what.
- American English spelling.

## Testing

`reconciler` is tested against controller-runtime's fake client with a
hand-written test resource and a fake target system. Each behavior a consumer
relies on (Observe never applies, the re-plan after apply, the pending cap,
revoke-then-release on delete, metric series dropped with the resource,
dependency order and skipping, prune-after-apply, a failed prune staying
owned) has a test, and the tests have been checked to fail when that behavior
is removed.

## Status

Prepared for publication at `github.com/blairham/k8s-controller-kit` but not
yet pushed. Until it is, consumers reference it with a `replace` directive;
`database-controller` moves onto it after publication, since its CI cannot
resolve a local path. Once public, `main` gets a repository ruleset like
tuikit's: squash-only, signed commits, linear history, stale reviews
dismissed, the CI and CodeQL checks required on an up-to-date branch, and no
bypass.
