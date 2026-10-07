# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-stable releases (`v0.x.y`) make no API-stability promise -- breaking changes can land in any `v0.x` bump.

## [Unreleased]

## [0.0.1] - 2026-10-07

### Fixed

- `reconciler`: a failed reconcile no longer reports an older generation's
  plan as current. `fail()` advances `observedGeneration`, and now also sets
  `Converged` to `Unknown` for that generation and clears `pending`,
  `pendingCount` and `warnings`. Before, a client waiting on
  `observedGeneration == generation` read the previous spec's refusals as the
  cause of the new failure (#7).

### Changed

- Dependencies: `sigs.k8s.io/controller-runtime` v0.25.1 → v0.25.2,
  `github.com/prometheus/client_golang` v1.24.0 → v1.24.1.

## [0.0.0] - 2026-10-06

### Added

- `plan`: `Plan` and `Step` -- a reviewable set of provisioning steps with a
  dry-run rendering (`Describe`), a fingerprint (`Hash`), and an `Apply` that
  reports applied, tolerated and best-effort-failed steps.
- `plan`: an optional dependency graph. Steps that implement `Identified`
  and `Dependent` run in dependency order (insertion order among ready
  steps, so a plan without dependencies is unchanged); `SetOutput` and
  `Ref` pass a value one step produces to a later one at apply time;
  dependents of a failed step are skipped with a warning; an invalid graph
  runs nothing. `Op` is a ready-made `Step`.
- `reconciler`: a generic reconciler for plan-driven resources -- Enforce
  and Observe modes, `Ready` and `Converged` conditions, a re-plan after
  apply, a pending list capped at 50, a finalizer only while deletion has
  work to do (patched with an optimistic lock), an hourly drift requeue,
  and per-resource Prometheus metrics.
- `reconciler`: ownership and pruning. With `Status.Owned` set and a
  `Session` that implements `Pruner`, the reconciler records the keys a
  resource establishes and prunes keys its spec no longer wants, after the
  main plan applies.
- `secret`: `Read` and `Get` read one key of a Secret in the resource's own
  namespace; `Get` also returns its `resourceVersion`.
- `manager`: `Main`, a shared controller binary entry point -- flags,
  `--controllers` selection (an unknown name refuses to start), leader
  election, and a readiness check that fails while a watched CRD is missing.
