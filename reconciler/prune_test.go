// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/blairham/k8s-controller-kit/reconciler"
)

// pruningSession owns every item in the spec, and prunes by deleting.
type pruningSession struct{ session }

func (s pruningSession) Owned(context.Context) ([]string, error) { return s.w.Spec.Want, nil }

func (s pruningSession) Prune(_ context.Context, stale []string) ([]reconciler.PruneStep, error) {
	var out []reconciler.PruneStep
	for _, k := range stale {
		if s.t.have[k] {
			out = append(out, pruneStep{step{t: s.t, item: k, remove: true, bestEffort: true}})
		}
	}
	return out, nil
}

type pruneStep struct{ step }

func (p pruneStep) Key() string { return p.item }

func (p pruneStep) Apply(ctx context.Context) error {
	if p.t.refuse["delete-"+p.item] {
		p.t.log = append(p.t.log, p.Describe())
		return context.DeadlineExceeded
	}
	return p.step.Apply(ctx)
}

func pruneHarness(t *testing.T, want ...string) *harness {
	t.Helper()
	h := newHarness(t, &widget{Spec: widgetSpec{Want: want}})
	h.t.prune = true
	return h
}

func setWant(t *testing.T, h *harness, want ...string) {
	t.Helper()
	w := h.get(t)
	w.Spec.Want = want
	if err := h.c.Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func TestRemovedFromSpecIsPruned(t *testing.T) {
	t.Parallel()
	h := pruneHarness(t, "a", "b")
	h.t.have["hand-made"] = true // never declared: never touched

	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if got := h.get(t).Status.Owned; !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("inventory = %v", got)
	}

	setWant(t, h, "a", "c")
	h.t.log = nil
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if h.t.have["b"] || !h.t.have["a"] || !h.t.have["c"] || !h.t.have["hand-made"] {
		t.Errorf("target = %v; want b pruned, a and c present, hand-made untouched", h.t.have)
	}
	// Cleanup runs after the main plan: c exists before b goes.
	if want := []string{"CREATE c", "DELETE b"}; !slices.Equal(h.t.log, want) {
		t.Errorf("writes = %v, want %v", h.t.log, want)
	}
	w := h.get(t)
	if !slices.Equal(w.Status.Owned, []string{"a", "c"}) || w.Status.PendingCount != 0 || w.Status.Applied != 2 {
		t.Errorf("status = %+v", w.Status)
	}
}

func TestObserveShowsThePruneAndKeepsTheInventory(t *testing.T) {
	t.Parallel()
	h := pruneHarness(t, "a", "b")
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	w := h.get(t)
	w.Spec.Want, w.Spec.Observe = []string{"a"}, true
	if err := h.c.Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	w = h.get(t)
	if !h.t.have["b"] {
		t.Error("observe pruned")
	}
	if !slices.Equal(w.Status.Pending, []string{"DELETE b"}) || !slices.Equal(w.Status.Owned, []string{"a", "b"}) {
		t.Errorf("pending %v, inventory %v", w.Status.Pending, w.Status.Owned)
	}
}

func TestFailedPruneStaysOwnedAndPending(t *testing.T) {
	t.Parallel()
	h := pruneHarness(t, "a", "b")
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	setWant(t, h, "a")
	h.t.refuse["delete-b"] = true
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	w := h.get(t)
	if !slices.Equal(w.Status.Owned, []string{"a", "b"}) {
		t.Errorf("inventory = %v; b is still there, so it must stay owned", w.Status.Owned)
	}
	if !slices.Equal(w.Status.Pending, []string{"DELETE b"}) || len(w.Status.Warnings) != 1 {
		t.Errorf("pending %v, warnings %v", w.Status.Pending, w.Status.Warnings)
	}
	if c := cond(t, w, reconciler.ConditionConverged); c.Status != metav1.ConditionFalse {
		t.Errorf("Converged = %+v", c)
	}

	delete(h.t.refuse, "delete-b")
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if w := h.get(t); h.t.have["b"] || !slices.Equal(w.Status.Owned, []string{"a"}) {
		t.Errorf("after retry: target %v, inventory %v", h.t.have, w.Status.Owned)
	}
}

func TestFatalMainPlanPrunesNothingButOwnsTheSpec(t *testing.T) {
	t.Parallel()
	h := pruneHarness(t, "a", "b")
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	setWant(t, h, "a", "c")
	h.t.refuse["c"] = true
	if _, err := h.reconcile(t); err == nil {
		t.Fatal("want the fatal apply error")
	}
	if !h.t.have["b"] {
		t.Error("pruned although the main plan failed")
	}
	if got := h.get(t).Status.Owned; !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("inventory = %v; must keep b and claim c", got)
	}
}

func TestWithoutInventoryNothingIsPruned(t *testing.T) {
	t.Parallel()
	h := pruneHarness(t, "a", "b")
	status := h.r.StatusOf
	h.r.StatusOf = func(w *widget) reconciler.Status {
		st := status(w)
		st.Owned = nil
		return st
	}
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	setWant(t, h, "a")
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if !h.t.have["b"] {
		t.Error("pruned with pruning off")
	}
}

func TestDeleteAlsoPrunesWhatTheSpecNoLongerDeclares(t *testing.T) {
	t.Parallel()
	h := pruneHarness(t, "a", "b")
	w := h.get(t)
	w.Spec.Revoke = true
	if err := h.c.Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	setWant(t, h, "a") // b is now only in the inventory
	if err := h.c.Delete(context.Background(), h.get(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if len(h.t.have) != 0 {
		t.Errorf("left after delete: %v", h.t.have)
	}
	if !strings.Contains(strings.Join(h.t.log, ","), "DELETE b") {
		t.Errorf("writes = %v", h.t.log)
	}
}
