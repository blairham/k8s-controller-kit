// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package reconciler drives a resource whose desired state is reached by a
// plan.Plan: read the target system, plan the difference, then either apply
// it (Enforce) or record it in status (Observe). The domain-specific parts --
// how a resource becomes a plan, and where its status fields live -- are
// supplied by the caller.
package reconciler

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/blairham/k8s-controller-kit/plan"
)

const (
	// ConditionReady reports whether the last reconcile planned, and in
	// Enforce applied, without a fatal error.
	ConditionReady = "Ready"

	// ConditionConverged reports whether the target already matches the spec:
	// True when the last plan was empty.
	ConditionConverged = "Converged"

	// DefaultDriftInterval is how often a resource is re-reconciled with no
	// event, so out-of-band changes get corrected.
	DefaultDriftInterval = time.Hour

	// maxPending caps the Pending list; the pending count stays exact.
	maxPending = 50
)

// Session is a target system opened for one resource. Plans are built from
// the resource as it was when the session was opened.
type Session interface {
	// Plan returns the steps that bring the target to the resource's spec.
	Plan(ctx context.Context) (*plan.Plan, error)

	// RevokePlan returns the steps that withdraw what Plan established.
	RevokePlan(ctx context.Context) (*plan.Plan, error)

	// Close releases the session's connection.
	Close() error
}

// Status points at the status fields the reconciler maintains, so each API
// keeps its own field names. Every pointer must be set.
type Status struct {
	Conditions         *[]metav1.Condition
	ObservedGeneration *int64
	AppliedPlanHash    *string
	LastAppliedTime    **metav1.Time
	Applied            *int
	Warnings           *[]string
	PendingCount       *int
	Pending            *[]string
	LastPlannedTime    **metav1.Time

	// Owned, when set, holds the inventory of keys the resource has
	// established, and turns on pruning for a Session that implements
	// Pruner. Leave it nil for a resource that never prunes.
	Owned *[]string
}

// Reconciler reconciles one plan-driven resource type. T is the pointer type,
// such as *v1alpha1.DatabaseAccess.
type Reconciler[T client.Object] struct {
	Client   client.Client
	Recorder record.EventRecorder

	// RevokeOnDelete reports whether deleting obj should run its revoke plan.
	RevokeOnDelete func(obj T) bool

	// New returns an empty T for Get to fill.
	New func() T

	// Open connects to the target system for obj.
	Open func(ctx context.Context, obj T) (Session, error)

	// Observing reports whether obj asks for plan-only reconciles.
	Observing func(obj T) bool

	// StatusOf returns pointers into obj's status.
	StatusOf func(obj T) Status

	// Metrics, when set, publishes per-resource series.
	Metrics *Metrics

	// BeforeStatusUpdate, when set, runs after a successful plan or apply and
	// before the status write, to record domain-specific status.
	BeforeStatusUpdate func(ctx context.Context, obj T, s Session)

	// Options are passed to the underlying controller. Tests set
	// SkipNameValidation because each runs its own manager.
	Options controller.Options

	// Name is the controller's name, unique within the manager.
	Name string

	// Finalizer is added only while deletion has work to do.
	Finalizer string

	// Noun names one plan step in messages ("statement", "operation"), and
	// Target the system planned against ("database", "cluster").
	Noun   string
	Target string

	// DriftInterval defaults to DefaultDriftInterval.
	DriftInterval time.Duration
}

// Reconcile brings the target system in line with the resource.
func (r *Reconciler[T]) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.New()
	if err := r.Client.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			// Drop its metric series, or a resource deleted while failing
			// would alert forever.
			r.Metrics.forget(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.reconcileDelete(ctx, obj)
	}

	// Only carry the finalizer when deletion has work to do; Observe never
	// revokes.
	if r.RevokeOnDelete(obj) && !r.Observing(obj) && !controllerutil.ContainsFinalizer(obj, r.Finalizer) {
		if err := r.setFinalizer(ctx, obj, true); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	s, err := r.Open(ctx, obj)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, "ConnectionFailed", err)
	}
	defer s.Close() //nolint:errcheck // close failure on a teardown path

	p, err := s.Plan(ctx)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, "PlanFailed", err)
	}
	pr, err := r.pruning(ctx, obj, s)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, "PlanFailed", err)
	}

	if r.Observing(obj) {
		return r.observe(ctx, obj, s, append(PendingOf(p), PendingOf(&pr.plan)...))
	}
	return r.enforce(ctx, obj, s, p, &pr)
}

func (r *Reconciler[T]) enforce(ctx context.Context, obj T, s Session, p *plan.Plan, pr *pruning) (ctrl.Result, error) {
	st := r.StatusOf(obj)
	if pr.on {
		// Recorded before applying, so a plan that fails half way still owns
		// what it may have created. Wanted keys that were never created cost
		// nothing: pruning a key the target lacks plans no step.
		*st.Owned = union(*st.Owned, pr.desired)
	}

	res, err := p.Apply(ctx)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, "ApplyFailed", err)
	}
	hash := p.Hash()

	// Cleanup runs only after the main plan succeeded, so whatever replaces
	// a stale thing exists before the stale thing goes.
	var stillStale []PruneStep
	if pr.on {
		pres, err := pr.plan.Apply(ctx)
		if err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, "PruneFailed", err)
		}
		res.Applied += pres.Applied
		res.Tolerated += pres.Tolerated
		res.Skipped += pres.Skipped
		res.Warnings = append(res.Warnings, pres.Warnings...)
		if pr.plan.Len() > 0 {
			var both plan.Plan
			both.Add(p.Steps()...)
			both.Add(pr.plan.Steps()...)
			hash = both.Hash()
		}
		if stillStale, err = pr.remaining(ctx); err == nil {
			*st.Owned = union(pr.desired, keysOf(stillStale))
		}
	}

	log.FromContext(ctx).Info("applied plan",
		"steps", res.Applied, "warnings", len(res.Warnings), "hash", hash)

	if len(res.Warnings) > 0 {
		r.Recorder.Eventf(obj, "Warning", "PartiallyApplied",
			"%d best-effort %s(s) were skipped (see status.warnings)", len(res.Warnings), r.noun())
	}

	now := metav1.Now()
	*st.ObservedGeneration = obj.GetGeneration()
	*st.AppliedPlanHash = hash
	*st.LastAppliedTime = &now
	*st.Applied = res.Applied
	*st.Warnings = res.Warnings
	ready := metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Applied",
		Message:            fmt.Sprintf("applied %d %s(s)", res.Applied, r.noun()),
		ObservedGeneration: obj.GetGeneration(),
	}
	if len(res.Warnings) > 0 {
		ready.Reason = "AppliedWithWarnings"
		ready.Message = fmt.Sprintf("applied %d %s(s), %d skipped", res.Applied, r.noun(), len(res.Warnings))
	}
	apimeta.SetStatusCondition(st.Conditions, ready)

	// Re-plan: a refused best-effort step, or one the target accepted without
	// effect, leaves it short of a successful apply.
	if again, err := s.Plan(ctx); err != nil {
		apimeta.SetStatusCondition(st.Conditions, metav1.Condition{
			Type:               ConditionConverged,
			Status:             metav1.ConditionUnknown,
			Reason:             "ReplanFailed",
			Message:            err.Error(),
			ObservedGeneration: obj.GetGeneration(),
		})
	} else {
		r.setPending(obj, append(PendingOf(again), describeAll(stillStale)...))
	}
	if r.BeforeStatusUpdate != nil {
		r.BeforeStatusUpdate(ctx, obj, s)
	}

	// Before the status write: the target already reflects the apply.
	r.Metrics.applied(obj.GetNamespace(), obj.GetName(), len(res.Warnings), now.Time)

	if err := r.Client.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return ctrl.Result{RequeueAfter: r.driftInterval()}, nil
}

// observe records what the plan would change and changes nothing. It receives
// only the pending list, never the plan, so it has nothing it could Apply.
func (r *Reconciler[T]) observe(ctx context.Context, obj T, s Session, pending []string) (ctrl.Result, error) {
	r.setPending(obj, pending)
	st := r.StatusOf(obj)
	*st.ObservedGeneration = obj.GetGeneration()
	// Clear per-apply fields; LastAppliedTime and AppliedPlanHash are history.
	*st.Applied = 0
	*st.Warnings = nil
	apimeta.SetStatusCondition(st.Conditions, metav1.Condition{
		Type:   ConditionReady,
		Status: metav1.ConditionTrue,
		Reason: "Observed",
		Message: fmt.Sprintf("observe mode: %d %s(s) pending; nothing was applied",
			len(pending), r.noun()),
		ObservedGeneration: obj.GetGeneration(),
	})
	if r.BeforeStatusUpdate != nil {
		r.BeforeStatusUpdate(ctx, obj, s)
	}
	r.Metrics.observed(obj.GetNamespace(), obj.GetName())

	if err := r.Client.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	log.FromContext(ctx).Info("observed plan", "pending", len(pending))
	return ctrl.Result{RequeueAfter: r.driftInterval()}, nil
}

// setPending records a plan's steps and the Converged condition.
func (r *Reconciler[T]) setPending(obj T, pending []string) {
	st := r.StatusOf(obj)
	now := metav1.Now()
	*st.LastPlannedTime = &now
	*st.PendingCount = len(pending)
	*st.Pending = nil
	if len(pending) > 0 {
		*st.Pending = append([]string(nil), pending[:min(len(pending), maxPending)]...)
	}
	r.Metrics.planned(obj.GetNamespace(), obj.GetName(), len(pending), now.Time)

	cond := metav1.Condition{
		Type:               ConditionConverged,
		Status:             metav1.ConditionTrue,
		Reason:             "Converged",
		Message:            fmt.Sprintf("the %s matches the spec; nothing is pending", r.target()),
		ObservedGeneration: obj.GetGeneration(),
	}
	if len(pending) > 0 {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "Pending"
		cond.Message = fmt.Sprintf("%d %s(s) pending, first: %s", len(pending), r.noun(), firstLine(pending[0]))
	}
	apimeta.SetStatusCondition(st.Conditions, cond)
}

func (r *Reconciler[T]) reconcileDelete(ctx context.Context, obj T) error {
	if !controllerutil.ContainsFinalizer(obj, r.Finalizer) {
		r.Metrics.forget(obj.GetNamespace(), obj.GetName())
		return nil
	}

	// A resource switched to Observe is released without revoking.
	if r.RevokeOnDelete(obj) && !r.Observing(obj) {
		if err := r.revoke(ctx, obj); err != nil {
			return err
		}
	}

	if err := r.setFinalizer(ctx, obj, false); err != nil {
		return fmt.Errorf("removing finalizer: %w", err)
	}
	r.Metrics.forget(obj.GetNamespace(), obj.GetName())
	return nil
}

// revoke runs the revoke plan, then prunes what the resource still owns but
// its spec no longer declares. Errors come back already recorded on obj.
func (r *Reconciler[T]) revoke(ctx context.Context, obj T) error {
	s, err := r.Open(ctx, obj)
	if err != nil {
		return r.fail(ctx, obj, "ConnectionFailed", err)
	}
	defer s.Close() //nolint:errcheck // close failure on a teardown path

	p, err := s.RevokePlan(ctx)
	if err != nil {
		return r.fail(ctx, obj, "RevokePlanFailed", err)
	}
	if _, err = p.Apply(ctx); err != nil {
		return r.fail(ctx, obj, "RevokeFailed", err)
	}
	// The revoke plan withdraws what the spec declares now; what it declared
	// before and still owns goes too.
	pr, err := r.pruning(ctx, obj, s)
	if err != nil {
		return r.fail(ctx, obj, "RevokePlanFailed", err)
	}
	if _, err = pr.plan.Apply(ctx); err != nil {
		return r.fail(ctx, obj, "RevokeFailed", err)
	}
	return nil
}

// setFinalizer adds (present) or removes the finalizer and copies the
// resulting metadata back into obj.
//
// It patches a fresh read rather than updating obj: on the delete path the
// revoke plan runs between the reconcile's read and this write, so obj's
// resourceVersion may be stale, and a full Update would conflict and send the
// whole reconcile -- the revoke included -- back to the queue. The patch keeps
// the optimistic lock, because a merge patch replaces metadata.finalizers
// wholesale and a lock-free one built from a stale list would drop or restore
// another controller's finalizer.
func (r *Reconciler[T]) setFinalizer(ctx context.Context, obj T, present bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := r.New()
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
			// Already gone: nothing left to release.
			if !present && apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		base, ok := fresh.DeepCopyObject().(client.Object)
		if !ok {
			return fmt.Errorf("deep copy of %T is not a client.Object", fresh)
		}
		var changed bool
		if present {
			changed = controllerutil.AddFinalizer(fresh, r.Finalizer)
		} else {
			changed = controllerutil.RemoveFinalizer(fresh, r.Finalizer)
		}
		if changed {
			patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
			if err := r.Client.Patch(ctx, fresh, patch); err != nil {
				return err
			}
		}
		// Carry the new resourceVersion forward, or the status write that
		// follows on the add path would conflict in turn.
		obj.SetFinalizers(fresh.GetFinalizers())
		obj.SetResourceVersion(fresh.GetResourceVersion())
		return nil
	})
}

// fail records the error on the resource and returns it, so the work queue
// retries with backoff.
func (r *Reconciler[T]) fail(ctx context.Context, obj T, reason string, cause error) error {
	// Before the status write, so the metric moves even if that write fails.
	r.Metrics.failed(obj.GetNamespace(), obj.GetName())

	st := r.StatusOf(obj)
	apimeta.SetStatusCondition(st.Conditions, metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            cause.Error(),
		ObservedGeneration: obj.GetGeneration(),
	})
	// ObservedGeneration moves to this generation below, so every field that
	// claims to describe the CURRENT spec must be rewritten here too, or it
	// keeps an older generation's answer under the new generation's number.
	// Converged and Pending are plan results for this spec, and no plan for it
	// succeeded: Unknown, and nothing listed. Warnings are per-apply, as
	// observe treats them, and no apply for this spec completed: none.
	// LastAppliedTime and AppliedPlanHash are history and stay.
	apimeta.SetStatusCondition(st.Conditions, metav1.Condition{
		Type:               ConditionConverged,
		Status:             metav1.ConditionUnknown,
		Reason:             reason,
		Message:            "not known: the last reconcile failed (see Ready)",
		ObservedGeneration: obj.GetGeneration(),
	})
	*st.PendingCount = 0
	*st.Pending = nil
	*st.Warnings = nil
	*st.ObservedGeneration = obj.GetGeneration()

	if err := r.Client.Status().Update(ctx, obj); err != nil {
		return fmt.Errorf("updating status after %s: %w (original error: %w)", reason, err, cause)
	}
	r.Recorder.Event(obj, "Warning", reason, cause.Error())
	return cause
}

// SetupWithManager registers the reconciler with the manager.
func (r *Reconciler[T]) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(r.New()).
		Named(r.Name).
		WithOptions(r.Options).
		Complete(r)
}

// PendingOf flattens a plan into the steps it would run.
func PendingOf(p *plan.Plan) []string {
	out := make([]string, 0, p.Len())
	for _, s := range p.Steps() {
		out = append(out, s.Describe())
	}
	return out
}

func (r *Reconciler[T]) noun() string {
	if r.Noun == "" {
		return "step"
	}
	return r.Noun
}

func (r *Reconciler[T]) target() string {
	if r.Target == "" {
		return "target"
	}
	return r.Target
}

func (r *Reconciler[T]) driftInterval() time.Duration {
	if r.DriftInterval == 0 {
		return DefaultDriftInterval
	}
	return r.DriftInterval
}

// firstLine trims a step to its first line for a condition message.
func firstLine(s string) string {
	for i := range len(s) {
		if s[i] == '\n' {
			return s[:i] + " ..."
		}
	}
	return s
}
