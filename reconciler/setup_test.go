// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/blairham/k8s-controller-kit/reconciler"
)

// The reconciler's own status write is a watch event. If it reconciled, an
// idle resource would be re-planned and re-applied against its target about
// once a second (#21). Only a generation change -- a spec edit, or deletion
// of an object with a finalizer -- may start one.
func TestSetupReconcilesOnlyOnAGenerationChange(t *testing.T) {
	t.Parallel()
	s := runtime.NewScheme()
	s.AddKnownTypes(gv, &widget{}, &widgetList{})
	metav1.AddToGroupVersion(s, gv)

	// Reconcile starts with a Get, so counting Gets counts reconciles.
	var reconciles atomic.Int32
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reconciles.Add(1)
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()

	informers := &informertest.FakeInformers{Scheme: s}
	inf, err := informers.FakeInformerFor(context.Background(), &widget{})
	if err != nil {
		t.Fatal(err)
	}
	watched := &watchedInformer{FakeInformer: inf, attached: make(chan struct{})}
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, ctrl.Options{
		Scheme:  s,
		Metrics: metricsserver.Options{BindAddress: "0"},
		NewCache: func(*rest.Config, cache.Options) (cache.Cache, error) {
			return &watchedCache{FakeInformers: informers, inf: watched}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &reconciler.Reconciler[*widget]{
		Client:   c,
		Recorder: events.NewFakeRecorder(10),
		Name:     "widget-setup",
		New:      func() *widget { return &widget{} },
		Options:  controller.Options{SkipNameValidation: ptr.To(true)},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-watched.attached:
	case <-time.After(5 * time.Second):
		t.Fatal("the controller never watched widgets")
	}

	// waitFor fails unless the count reaches want within a second, then
	// checks it stays there, so a late extra reconcile is caught too.
	waitFor := func(what string, want int32) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for reconciles.Load() < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		if got := reconciles.Load(); got != want {
			t.Fatalf("after %s: %d reconciles, want %d", what, got, want)
		}
	}

	w := &widget{}
	w.Name, w.Namespace, w.Generation, w.ResourceVersion = "w", "ns", 1, "1"
	inf.Add(w)
	waitFor("create", 1)

	status := w.DeepCopyObject().(*widget) //nolint:forcetypeassert // DeepCopyObject returns *widget
	status.ResourceVersion = "2"
	status.Status.LastPlanned = &metav1.Time{Time: time.Now()}
	inf.Update(w, status)
	waitFor("a status-only write", 1)

	spec := status.DeepCopyObject().(*widget) //nolint:forcetypeassert // as above
	spec.ResourceVersion, spec.Generation = "3", 2
	spec.Spec.Want = []string{"a"}
	inf.Update(status, spec)
	waitFor("a spec change", 2)

	deleting := spec.DeepCopyObject().(*widget) //nolint:forcetypeassert // as above
	deleting.ResourceVersion, deleting.Generation = "4", 3
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	deleting.Finalizers = []string{finalizer}
	inf.Update(spec, deleting)
	waitFor("deletion", 3)
}

// watchedCache hands out one informer and reports when the controller has
// attached its handler, so the test's events are not sent to nobody.
type watchedCache struct {
	*informertest.FakeInformers
	inf *watchedInformer
}

func (c *watchedCache) GetInformer(context.Context, client.Object, ...cache.InformerGetOption) (cache.Informer, error) {
	return c.inf, nil
}

type watchedInformer struct {
	*controllertest.FakeInformer
	attached chan struct{}
}

func (i *watchedInformer) AddEventHandlerWithOptions(
	h toolscache.ResourceEventHandler, o toolscache.HandlerOptions,
) (toolscache.ResourceEventHandlerRegistration, error) {
	reg, err := i.FakeInformer.AddEventHandlerWithOptions(h, o)
	close(i.attached)
	return reg, err
}
