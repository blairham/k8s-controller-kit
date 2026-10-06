// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/blairham/k8s-controller-kit/plan"
	"github.com/blairham/k8s-controller-kit/reconciler"
)

// widget is a minimal plan-driven resource for the tests.
type widget struct {
	metav1.TypeMeta   `             json:",inline"`
	metav1.ObjectMeta `             json:"metadata,omitempty"`
	Spec              widgetSpec   `json:"spec"`
	Status            widgetStatus `json:"status"`
}

type widgetSpec struct {
	Want    []string `json:"want,omitempty"`
	Observe bool     `json:"observe,omitempty"`
	Revoke  bool     `json:"revoke,omitempty"`
}

type widgetStatus struct {
	LastApplied        *metav1.Time       `json:"lastApplied,omitempty"`
	LastPlanned        *metav1.Time       `json:"lastPlanned,omitempty"`
	Hash               string             `json:"hash,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	Warnings           []string           `json:"warnings,omitempty"`
	Pending            []string           `json:"pending,omitempty"`
	Owned              []string           `json:"owned,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Applied            int                `json:"applied,omitempty"`
	PendingCount       int                `json:"pendingCount"`
}

func (w *widget) DeepCopyObject() runtime.Object {
	out := *w
	w.DeepCopyInto(&out.ObjectMeta)
	out.Spec.Want = append([]string(nil), w.Spec.Want...)
	out.Status.Conditions = append([]metav1.Condition(nil), w.Status.Conditions...)
	out.Status.Warnings = append([]string(nil), w.Status.Warnings...)
	out.Status.Pending = append([]string(nil), w.Status.Pending...)
	out.Status.Owned = append([]string(nil), w.Status.Owned...)
	if w.Status.LastApplied != nil {
		out.Status.LastApplied = w.Status.LastApplied.DeepCopy()
	}
	if w.Status.LastPlanned != nil {
		out.Status.LastPlanned = w.Status.LastPlanned.DeepCopy()
	}
	return &out
}

type widgetList struct {
	metav1.TypeMeta `         json:",inline"`
	metav1.ListMeta `         json:"metadata,omitempty"`
	Items           []widget `json:"items"`
}

func (l *widgetList) DeepCopyObject() runtime.Object {
	out := &widgetList{TypeMeta: l.TypeMeta}
	l.DeepCopyInto(&out.ListMeta)
	for i := range l.Items {
		w, _ := l.Items[i].DeepCopyObject().(*widget)
		out.Items = append(out.Items, *w)
	}
	return out
}

var gv = schema.GroupVersion{Group: "test.k8s-controller-kit.io", Version: "v1"}

// target is a fake system: a set of items, some of which refuse to be created.
type target struct {
	planErr error
	have    map[string]bool
	refuse  map[string]bool
	log     []string
	opened  int
	revoked bool
	prune   bool
}

type step struct {
	t          *target
	item       string
	bestEffort bool
	remove     bool
}

func (s step) Describe() string {
	if s.remove {
		return "DELETE " + s.item
	}
	return "CREATE " + s.item
}
func (step) Rationale() string    { return "" }
func (s step) IsBestEffort() bool { return s.bestEffort }
func (step) Tolerates(error) bool { return false }
func (s step) Apply(context.Context) error {
	s.t.log = append(s.t.log, s.Describe())
	if s.remove {
		delete(s.t.have, s.item)
		s.t.revoked = true
		return nil
	}
	if s.t.refuse[s.item] {
		return fmt.Errorf("refused %s", s.item)
	}
	s.t.have[s.item] = true
	return nil
}

type session struct {
	t *target
	w *widget
}

func (s session) Plan(context.Context) (*plan.Plan, error) {
	if s.t.planErr != nil {
		return nil, s.t.planErr
	}
	var p plan.Plan
	for _, item := range s.w.Spec.Want {
		if !s.t.have[item] {
			p.Add(step{t: s.t, item: item, bestEffort: strings.HasPrefix(item, "soft-")})
		}
	}
	return &p, nil
}

func (s session) RevokePlan(context.Context) (*plan.Plan, error) {
	var p plan.Plan
	for _, item := range s.w.Spec.Want {
		p.Add(step{t: s.t, item: item, remove: true})
	}
	return &p, nil
}

func (session) Close() error { return nil }

type harness struct {
	c       client.Client
	r       *reconciler.Reconciler[*widget]
	t       *target
	reg     *prometheus.Registry
	metrics *reconciler.Metrics
}

const finalizer = "test.k8s-controller-kit.io/revoke"

func newHarness(tb testing.TB, w *widget) *harness {
	tb.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypes(gv, &widget{}, &widgetList{})
	metav1.AddToGroupVersion(s, gv)

	w.Name, w.Namespace = "w", "ns"
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&widget{}).WithObjects(w).Build()
	tgt := &target{have: map[string]bool{}, refuse: map[string]bool{}}
	reg := prometheus.NewRegistry()
	m := reconciler.NewMetrics(reg, reconciler.MetricsConfig{Prefix: "test", Kind: "Widget", Noun: "operation"})
	r := &reconciler.Reconciler[*widget]{
		Client:   c,
		Recorder: record.NewFakeRecorder(100),
		Name:     "widget",
		New:      func() *widget { return &widget{} },
		Open: func(_ context.Context, w *widget) (reconciler.Session, error) {
			tgt.opened++
			if tgt.prune {
				return pruningSession{session{t: tgt, w: w}}, nil
			}
			return session{t: tgt, w: w}, nil
		},
		Observing:      func(w *widget) bool { return w.Spec.Observe },
		RevokeOnDelete: func(w *widget) bool { return w.Spec.Revoke },
		StatusOf: func(w *widget) reconciler.Status {
			st := &w.Status
			return reconciler.Status{
				Conditions:         &st.Conditions,
				ObservedGeneration: &st.ObservedGeneration,
				AppliedPlanHash:    &st.Hash,
				LastAppliedTime:    &st.LastApplied,
				Applied:            &st.Applied,
				Warnings:           &st.Warnings,
				PendingCount:       &st.PendingCount,
				Pending:            &st.Pending,
				LastPlannedTime:    &st.LastPlanned,
				Owned:              &st.Owned,
			}
		},
		Finalizer: finalizer,
		Metrics:   m,
		Noun:      "operation",
		Target:    "cluster",
	}
	return &harness{c: c, r: r, t: tgt, reg: reg, metrics: m}
}

func (h *harness) reconcile(tb testing.TB) (ctrl.Result, error) {
	tb.Helper()
	return h.r.Reconcile(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "w"}},
	)
}

func (h *harness) get(tb testing.TB) *widget {
	tb.Helper()
	var w widget
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "w"}, &w); err != nil {
		tb.Fatalf("get: %v", err)
	}
	return &w
}

func cond(tb testing.TB, w *widget, typ string) metav1.Condition {
	tb.Helper()
	c := apimeta.FindStatusCondition(w.Status.Conditions, typ)
	if c == nil {
		tb.Fatalf("no %s condition in %+v", typ, w.Status.Conditions)
	}
	return *c
}

func gauge(tb testing.TB, reg *prometheus.Registry, name string) float64 {
	tb.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		tb.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	tb.Fatalf("no series %s", name)
	return 0
}

func TestEnforceAppliesAndConverges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Want: []string{"a", "b"}}})

	res, err := h.reconcile(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != reconciler.DefaultDriftInterval {
		t.Errorf("RequeueAfter = %v, want the drift interval", res.RequeueAfter)
	}
	if !h.t.have["a"] || !h.t.have["b"] {
		t.Fatalf("target = %v, want a and b created", h.t.have)
	}
	w := h.get(t)
	if c := cond(t, w, reconciler.ConditionReady); c.Status != metav1.ConditionTrue || c.Reason != "Applied" ||
		c.Message != "applied 2 operation(s)" {
		t.Errorf("Ready = %+v", c)
	}
	if c := cond(t, w, reconciler.ConditionConverged); c.Status != metav1.ConditionTrue ||
		c.Message != "the cluster matches the spec; nothing is pending" {
		t.Errorf("Converged = %+v", c)
	}
	if w.Status.Applied != 2 || w.Status.Hash == "" || w.Status.LastApplied == nil || w.Status.LastPlanned == nil {
		t.Errorf("status = %+v", w.Status)
	}
	if w.Status.PendingCount != 0 || len(w.Finalizers) != 0 {
		t.Errorf("pending = %d, finalizers = %v", w.Status.PendingCount, w.Finalizers)
	}
	if got := gauge(t, h.reg, "test_access_ready"); got != 1 {
		t.Errorf("ready gauge = %v", got)
	}
	if got := testutil.CollectAndCount(h.reg, "test_access_pending_operations"); got != 1 {
		t.Errorf("pending series = %d, want 1", got)
	}

	// A second reconcile has nothing to do.
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if w := h.get(t); w.Status.Applied != 0 {
		t.Errorf("converged reconcile applied %d", w.Status.Applied)
	}
}

func TestObserveNeverApplies(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Observe: true, Revoke: true, Want: []string{"a"}}})

	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if len(h.t.have) != 0 {
		t.Fatalf("observe changed the target: %v", h.t.have)
	}
	w := h.get(t)
	if c := cond(t, w, reconciler.ConditionReady); c.Reason != "Observed" || c.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v", c)
	}
	if c := cond(t, w, reconciler.ConditionConverged); c.Status != metav1.ConditionFalse ||
		c.Message != "1 operation(s) pending, first: CREATE a" {
		t.Errorf("Converged = %+v", c)
	}
	if w.Status.PendingCount != 1 || len(w.Status.Pending) != 1 || w.Status.Pending[0] != "CREATE a" {
		t.Errorf("pending = %d %v", w.Status.PendingCount, w.Status.Pending)
	}
	// Observe never revokes, so it never needs the finalizer.
	if len(w.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none in Observe", w.Finalizers)
	}
}

func TestBestEffortFailureIsAWarningAndStaysPending(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Want: []string{"a", "soft-b"}}})
	h.t.refuse["soft-b"] = true

	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	w := h.get(t)
	if c := cond(t, w, reconciler.ConditionReady); c.Reason != "AppliedWithWarnings" ||
		c.Message != "applied 1 operation(s), 1 skipped" {
		t.Errorf("Ready = %+v", c)
	}
	if len(w.Status.Warnings) != 1 || !strings.Contains(w.Status.Warnings[0], "refused soft-b") {
		t.Errorf("warnings = %v", w.Status.Warnings)
	}
	// The re-plan after applying is what reports the gap.
	if c := cond(t, w, reconciler.ConditionConverged); c.Status != metav1.ConditionFalse {
		t.Errorf("Converged = %+v, want False", c)
	}
	if got := gauge(t, h.reg, "test_access_warnings"); got != 1 {
		t.Errorf("warnings gauge = %v", got)
	}
}

func TestFatalApplyFailsTheReconcile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Want: []string{"a"}}})
	h.t.refuse["a"] = true

	if _, err := h.reconcile(t); err == nil {
		t.Fatal("want an error for the work queue to retry")
	}
	w := h.get(t)
	if c := cond(t, w, reconciler.ConditionReady); c.Status != metav1.ConditionFalse || c.Reason != "ApplyFailed" {
		t.Errorf("Ready = %+v", c)
	}
	if got := gauge(t, h.reg, "test_access_ready"); got != 0 {
		t.Errorf("ready gauge = %v, want 0", got)
	}
}

func TestPlanErrorIsPlanFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Want: []string{"a"}}})
	h.t.planErr = errors.New("cluster unreachable")

	if _, err := h.reconcile(t); err == nil {
		t.Fatal("want an error")
	}
	if c := cond(t, h.get(t), reconciler.ConditionReady); c.Reason != "PlanFailed" || c.Message != "cluster unreachable" {
		t.Errorf("Ready = %+v", c)
	}
}

func TestRevokeOnDeleteRunsTheRevokePlan(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Revoke: true, Want: []string{"a"}}})

	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if w := h.get(t); len(w.Finalizers) != 1 || w.Finalizers[0] != finalizer {
		t.Fatalf("finalizers = %v", w.Finalizers)
	}
	if err := h.c.Delete(context.Background(), h.get(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if !h.t.revoked || h.t.have["a"] {
		t.Errorf("revoke did not run: %+v", h.t)
	}
	var w widget
	err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "w"}, &w)
	if err == nil {
		t.Errorf("resource still present with finalizers %v", w.Finalizers)
	}
	// The resource is gone, so its series must be too.
	if got := testutil.CollectAndCount(h.reg); got != 0 {
		t.Errorf("%d series left after delete", got)
	}
}

func TestPendingIsCappedButCountIsExact(t *testing.T) {
	t.Parallel()
	want := make([]string, 60)
	for i := range want {
		want[i] = fmt.Sprintf("i%02d", i)
	}
	h := newHarness(t, &widget{Spec: widgetSpec{Observe: true, Want: want}})
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	w := h.get(t)
	if w.Status.PendingCount != 60 || len(w.Status.Pending) != 50 {
		t.Errorf("pending = %d, listed %d; want 60 and 50", w.Status.PendingCount, len(w.Status.Pending))
	}
}

func TestNilMetricsIsAllowed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, &widget{Spec: widgetSpec{Want: []string{"a"}}})
	h.r.Metrics = nil
	if _, err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
}
