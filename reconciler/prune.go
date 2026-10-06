// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"

	"github.com/blairham/k8s-controller-kit/plan"
)

// Ownership and pruning
//
// A resource that removes something from its spec should have that thing
// removed from the target, but only if the resource established it: anything
// else may belong to another resource or a person. The reconciler keeps that
// record itself, as an inventory of keys in Status.Owned. On each Enforce
// reconcile the keys the spec still wants are added to it; keys in it the
// spec no longer wants are stale, and the session prunes them in a cleanup
// phase after the main plan has applied -- so whatever replaces a stale thing
// exists before it goes. A key leaves the inventory only once the target no
// longer has it.
//
// The target is not tagged (a Kafka ACL or a PostgreSQL grant cannot carry a
// tag), so a key the spec wants is adopted even if something else created it
// first. Two resources that declare the same thing therefore both own it, and
// removing it from one prunes it until the other's next reconcile restores it.

// Pruner is implemented by a Session whose engine can prune. Pruning is on
// only when the Session implements it and Status.Owned is set.
type Pruner interface {
	// Owned returns the keys of everything the spec establishes that the
	// resource should own: the keys that would be pruned if dropped. Keys
	// are opaque to the reconciler; the engine must be able to parse them
	// back, including keys an older spec produced.
	Owned(ctx context.Context) ([]string, error)

	// Prune returns one step per key in stale that the target still has.
	// A key the target no longer has gets no step; that is how a key leaves
	// the inventory. A key the engine cannot parse is skipped.
	Prune(ctx context.Context, stale []string) ([]PruneStep, error)
}

// PruneStep removes the thing named by Key.
type PruneStep interface {
	plan.Step
	Key() string
}

// pruning is one reconcile's view of the inventory.
type pruning struct {
	pruner  Pruner
	desired []string
	stale   []string
	plan    plan.Plan
	on      bool
}

func (r *Reconciler[T]) pruning(ctx context.Context, obj T, s Session) (pruning, error) {
	st := r.StatusOf(obj)
	pr, ok := s.(Pruner)
	if st.Owned == nil || !ok {
		return pruning{}, nil
	}
	desired, err := pr.Owned(ctx)
	if err != nil {
		return pruning{}, err
	}
	desired = sortedSet(desired)
	stale := minus(*st.Owned, desired)
	p := pruning{on: true, pruner: pr, desired: desired, stale: stale}
	if len(stale) == 0 {
		return p, nil
	}
	steps, err := pr.Prune(ctx, stale)
	if err != nil {
		return pruning{}, err
	}
	for _, s := range steps {
		p.plan.Add(s)
	}
	return p, nil
}

// remaining re-asks which stale keys the target still has, after a prune.
func (p *pruning) remaining(ctx context.Context) ([]PruneStep, error) {
	if len(p.stale) == 0 {
		return nil, nil
	}
	return p.pruner.Prune(ctx, p.stale)
}

func keysOf(steps []PruneStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Key()
	}
	return out
}

func describeAll(steps []PruneStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Describe()
	}
	return out
}

func sortedSet(keys []string) []string {
	out := slices.Clone(keys)
	slices.Sort(out)
	return slices.Compact(out)
}

func union(a, b []string) []string {
	return sortedSet(append(slices.Clone(a), b...))
}

func minus(a, b []string) []string {
	var out []string
	for _, k := range sortedSet(a) {
		if _, found := slices.BinarySearch(b, k); !found {
			out = append(out, k)
		}
	}
	return out
}
