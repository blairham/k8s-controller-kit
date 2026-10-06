# k8s-controller-kit

[![CI](https://github.com/blairham/k8s-controller-kit/actions/workflows/ci.yml/badge.svg)](https://github.com/blairham/k8s-controller-kit/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/tag/blairham/k8s-controller-kit?sort=semver&label=release)](https://github.com/blairham/k8s-controller-kit/tags)
[![CodeQL](https://github.com/blairham/k8s-controller-kit/actions/workflows/codeql.yml/badge.svg)](https://github.com/blairham/k8s-controller-kit/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/blairham/k8s-controller-kit/badge)](https://scorecard.dev/viewer/?uri=github.com/blairham/k8s-controller-kit)
[![Go version](https://img.shields.io/github/go-mod/go-version/blairham/k8s-controller-kit)](go.mod)
[![Go Reference](https://pkg.go.dev/badge/github.com/blairham/k8s-controller-kit.svg)](https://pkg.go.dev/github.com/blairham/k8s-controller-kit)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Building blocks for **plan-driven** Kubernetes controllers, on
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).
A plan-driven controller reads the system it manages, plans the difference
from a resource's spec as a reviewable list of steps, and either applies it
(`Enforce`) or records it in status without writing (`Observe`).

> **Status:** v0.0.x -- pre-stable. The API is being shaped by the
> controllers built on it; expect breaking changes until `v0.1.0`.

## What's in the box

| Package      | What it gives a controller |
|--------------|----------------------------|
| `plan`       | `Plan`/`Step`: dry-run text, a hash, and an `Apply` with best-effort and tolerated steps. Optionally a dependency graph: steps run in dependency order and pass values forward (`SetOutput`/`Ref`), and dependents of a failed step are skipped. |
| `reconciler` | The reconcile loop: Enforce/Observe, `Ready` and `Converged` conditions, a re-plan after apply, a finalizer only while deletion has work, hourly drift correction, per-resource metrics -- and **ownership and pruning**: what a resource established and its spec no longer declares is removed after the main plan applies. |
| `secret`     | Read one key of a Secret in the resource's own namespace. |
| `manager`    | A shared `main`: flags, `--controllers`, leader election, and a readiness check that fails while a CRD is missing. |

## How it works

The shape is the one
[aws-load-balancer-controller](https://github.com/kubernetes-sigs/aws-load-balancer-controller)
uses -- build the desired state, diff it against what exists, change only the
difference -- made generic and made visible:

1. Your **engine** opens a `reconciler.Session` for a resource and returns a
   `plan.Plan`: only the steps the target lacks. A converged target plans
   nothing.
2. In **Enforce** the reconciler applies it, then prunes what the resource
   owns but no longer declares, then re-plans to record anything still
   pending. In **Observe** it records the plan and writes nothing.
3. Status carries what happened: `Ready`, `Converged`, the pending steps,
   the warnings, and the inventory of what the resource owns.

Your API types stay yours: the reconciler reaches status through
`reconciler.Status`, a set of pointers into your struct, so no CRD embeds a
kit type.

```go
r := &reconciler.Reconciler[*v1alpha1.Widget]{
    Client:         mgr.GetClient(),
    Recorder:       mgr.GetEventRecorderFor("widget"),
    Name:           "widget",
    New:            func() *v1alpha1.Widget { return &v1alpha1.Widget{} },
    Open:           openSession, // your engine, bound to one resource
    Observing:      func(w *v1alpha1.Widget) bool { return w.Spec.Mode == "Observe" },
    RevokeOnDelete: func(w *v1alpha1.Widget) bool { return w.Spec.RevokeOnDelete },
    StatusOf: func(w *v1alpha1.Widget) reconciler.Status {
        st := &w.Status
        return reconciler.Status{
            Conditions: &st.Conditions, ObservedGeneration: &st.ObservedGeneration,
            AppliedPlanHash: &st.AppliedPlanHash, LastAppliedTime: &st.LastAppliedTime,
            Applied: &st.Applied, Warnings: &st.Warnings, PendingCount: &st.PendingCount,
            Pending: &st.Pending, LastPlannedTime: &st.LastPlannedTime,
            Owned: &st.Owned, // optional: turns on pruning
        }
    },
    Finalizer: "widgets.example.com/revoke",
    Metrics:   reconciler.NewMetrics(metrics.Registry, reconciler.MetricsConfig{Prefix: "widget_controller", Kind: "Widget", Noun: "step"}),
}
```

See [`plan`'s example](https://pkg.go.dev/github.com/blairham/k8s-controller-kit/plan#example-Ref)
for the dependency graph, and [`SECURITY.md`](SECURITY.md) for what the kit
does with a controller's credentials -- in particular, who may write the
inventory pruning trusts.

## Used by

- [database-controller](https://github.com/blairham/database-controller) --
  PostgreSQL roles, schemas and grants (moving onto the kit)
- kafka-controller -- Kafka topics, ACLs and SCRAM credentials

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md). Contributions need a signed
[CLA](CLA.md).

## License

Licensed under the Apache License, Version 2.0. See [`LICENSE`](LICENSE).
