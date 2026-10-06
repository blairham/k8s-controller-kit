# Contributing to k8s-controller-kit

Thanks for looking. Issues, bug reports and pull requests are all welcome.

k8s-controller-kit is pre-stable `v0.0.x`: the API is still being shaped by
the controllers built on it, and `v0.1.0` tags once it settles. Breaking
changes can land in any release until then, so small, additive PRs are the
easiest to take.

## Before you open a PR

```sh
pre-commit install   # once per clone: formatting, golangci-lint, secrets, YAML, license headers, govulncheck
make test            # go test -race ./...
```

golangci-lint runs as the commit hook and in CI, not as a make target; a
failing hook fails the commit, and you fix it and commit again. `make fmt`
applies gofumpt.

## What belongs here

Only what is engine-neutral and used, or about to be used, by more than one
controller: the plan, the reconcile loop, ownership and pruning, the shared
`main`. Anything that names a domain concept -- a PostgreSQL role, a Kafka
ACL, a load balancer -- belongs in that controller. New dependencies need a
reason; today there are controller-runtime, client-go and Prometheus.

## The shape of a change

[`AGENTS.md`](AGENTS.md) is the architecture guide. The rules that most often
decide a review:

- **Consumers keep their own API types.** The reconciler reaches a
  resource's status through `reconciler.Status`, a set of pointers, so no
  CRD embeds a kit type or changes its JSON field names to use the kit.
- **What consumers see is a contract.** A condition reason, a message
  shape, a metric name, or the hash of a plan without dependencies is read
  by someone's alert or status. Changing one is a breaking change; say so
  in the PR and the CHANGELOG.
- **Behavior that exists because of a failure keeps its test.** The comment
  on `setFinalizer` is an example: the lock is there because the revoke
  plan ran twice without it.
- Every exported change gets a line under `[Unreleased]` in
  [`CHANGELOG.md`](CHANGELOG.md).

## Tests

**New functionality comes with tests in the same pull request, and a bug fix
comes with a test that fails without the fix.** A PR that adds an exported
function, option or reconciler behavior without a test that exercises it is
not ready to merge.

- The reconciler is tested against controller-runtime's fake client with a
  test resource and a fake target system (`reconciler/reconciler_test.go`);
  a new behavior gets a case there.
- Code that takes input a controller does not control -- a plan's graph, an
  inventory from status -- has a fuzz target (`go test -fuzz`) that checks
  a property, not just the absence of a panic.
- Tests must never touch real user state -- use `t.TempDir()` and
  `t.Setenv`.

## The Contributor License Agreement

Contributions require a signed CLA; the text is in [`CLA.md`](CLA.md).

**Why.** The project may need to offer different licensing terms in future.
That is only possible if one party can license the whole work, and copyright
in a contribution stays with its author unless licensed onward.

The CLA does **not** take your copyright. You keep it; you grant a license
broad enough to include sublicensing, and you affirm the work is your own --
including that no employer holds rights to it.

## Commits and PRs

- Prefix the subject with the package it touches (`plan:`, `reconciler:`,
  `secret:`, `manager:`, `docs:`, `ci:`).
- Explain the **why** in the commit message. The diff already says what.
- One change per PR, and put `Closes #N` in the PR body.
- Commits must be signed.
- Every `.go` file carries the two-line SPDX header (`Apache-2.0`); the
  pre-commit hook fails without it.

## Releasing

Maintainers only. A release is a signed, annotated `vX.Y.Z` tag on `main`,
on the commit that moves the CHANGELOG's `[Unreleased]` section under
`## [X.Y.Z] - <date>`. Pushing the tag runs the release workflow, which
publishes that section as the notes, the source archive, a cosign-signed
`checksums.txt` and build provenance. Nothing is compiled.
