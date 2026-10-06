// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package manager is the shared main for a controller binary: flags, leader
// election, --controllers selection and a readiness check that fails while a
// watched CRD is missing.
package manager

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Controller is one controller the binary can run.
type Controller struct {
	// Watches is the primary resource, checked by readiness.
	Watches client.Object

	// Setup registers the controller with the manager.
	Setup func(mgr ctrl.Manager) error
}

// Config describes the binary.
type Config struct {
	Scheme *runtime.Scheme

	// Controllers maps a --controllers name to its controller. All run by
	// default.
	Controllers map[string]Controller

	// LeaderElectionID is the lease name, such as "database-controller.io".
	LeaderElectionID string
}

// Main parses flags from args and runs the manager until a signal.
func Main(cfg Config, args []string) error {
	names := make([]string, 0, len(cfg.Controllers))
	for n := range cfg.Controllers {
		names = append(names, n)
	}
	sort.Strings(names)

	fs := flag.NewFlagSet("manager", flag.ContinueOnError)
	var (
		metricsAddr, probeAddr, enabledFlag, watchNS string
		leaderElect                                  bool
		zapOpts                                      zap.Options
	)
	fs.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address the metric endpoint binds to")
	fs.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address the probe endpoint binds to")
	fs.BoolVar(&leaderElect, "leader-elect", false, "enable leader election, ensuring only one manager is active")
	fs.StringVar(&enabledFlag, "controllers", strings.Join(names, ","), "comma-separated controllers to run")
	fs.StringVar(&watchNS, "watch-namespace", "", "restrict the manager to one namespace; empty watches all")
	zapOpts.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	enabled, err := selectControllers(cfg.Controllers, enabledFlag)
	if err != nil {
		return err
	}

	options := ctrl.Options{
		Scheme:                 cfg.Scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       cfg.LeaderElectionID,
	}
	if watchNS != "" {
		options.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{watchNS: {}}}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	var watched []client.Object
	for _, n := range enabled {
		c := cfg.Controllers[n]
		if err := c.Setup(mgr); err != nil {
			return fmt.Errorf("setting up controller %s: %w", n, err)
		}
		watched = append(watched, c.Watches)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("adding health check: %w", err)
	}
	// Readiness means the watched types resolve, not a ping: with the CRD
	// absent the manager never starts workers but a ping still passes, and so
	// does WaitForCacheSync (no informer was registered). GetInformer goes
	// through the RESTMapper and fails.
	if err := mgr.AddReadyzCheck("readyz", func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()
		for _, obj := range watched {
			if _, err := mgr.GetCache().GetInformer(ctx, obj); err != nil {
				return fmt.Errorf("cannot watch %T; is its CRD installed? %w", obj, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("adding ready check: %w", err)
	}

	ctrl.Log.WithName("setup").Info("starting manager", "controllers", enabled)
	return mgr.Start(ctrl.SetupSignalHandler())
}

// selectControllers parses --controllers. An unknown name is a typo; refuse
// rather than silently run nothing.
func selectControllers(known map[string]Controller, flagValue string) ([]string, error) {
	var out, unknown []string
	for _, n := range strings.Split(flagValue, ",") {
		n = strings.TrimSpace(n)
		if n == "" || slices.Contains(out, n) {
			continue
		}
		if _, ok := known[n]; !ok {
			unknown = append(unknown, n)
			continue
		}
		out = append(out, n)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown controllers: %s", strings.Join(unknown, ", "))
	}
	if len(out) == 0 {
		return nil, errors.New("--controllers selects no controller")
	}
	return out, nil
}
