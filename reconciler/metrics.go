// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are per-resource series, so an alert can name the resource that
// lost access.
//
// Labels are only {namespace, name}: a label whose value changes (a reason, a
// principal) would leave a stale series behind that keeps an alert firing.
// Only the leader reconciles, so only the leader exports these.
type Metrics struct {
	ready, warnings, lastApplied, lastPlanned, pending *prometheus.GaugeVec
}

// MetricsConfig names the series. Prefix is the metric namespace, such as
// "database_controller"; the series are <prefix>_access_ready and so on.
type MetricsConfig struct {
	Prefix string

	// Kind is the resource kind for help text, such as "DatabaseAccess".
	Kind string

	// Noun names one plan step in help text, such as "statement".
	Noun string
}

// NewMetrics creates the series and registers them with reg, typically
// sigs.k8s.io/controller-runtime/pkg/metrics.Registry.
func NewMetrics(reg prometheus.Registerer, c MetricsConfig) *Metrics {
	labels := []string{"namespace", "name"}
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: c.Prefix,
			Name:      name,
			Help:      help,
		}, labels)
	}
	m := &Metrics{
		ready: gauge("access_ready",
			"1 when the "+c.Kind+"'s last reconcile succeeded (Ready=True: applied in Enforce, "+
				"planned in Observe), 0 otherwise."),
		warnings: gauge("access_warnings",
			"Best-effort "+c.Noun+"s skipped on the last successful apply (see status.warnings)."),
		lastApplied: gauge("access_last_applied_timestamp_seconds",
			"Unix time of the last successful apply. Never moves in Observe mode; alert on "+
				"last_planned for liveness."),
		lastPlanned: gauge("access_last_planned_timestamp_seconds",
			"Unix time the target was last read and planned against (status.lastPlannedTime). "+
				"With a one-hour drift interval, an age well past an hour means the resource has "+
				"stopped being reconciled."),
		pending: gauge("access_pending_"+c.Noun+"s",
			c.Noun+"s the target still needs to match the spec, from the last plan. 0 means converged."),
	}
	reg.MustRegister(m.ready, m.warnings, m.lastApplied, m.lastPlanned, m.pending)
	return m
}

func (m *Metrics) applied(namespace, name string, warnings int, at time.Time) {
	if m == nil {
		return
	}
	m.ready.WithLabelValues(namespace, name).Set(1)
	m.warnings.WithLabelValues(namespace, name).Set(float64(warnings))
	m.lastApplied.WithLabelValues(namespace, name).Set(float64(at.Unix()))
}

// observed publishes a successful Observe reconcile: Ready, no warnings,
// last_applied untouched.
func (m *Metrics) observed(namespace, name string) {
	if m == nil {
		return
	}
	m.ready.WithLabelValues(namespace, name).Set(1)
	m.warnings.WithLabelValues(namespace, name).Set(0)
}

func (m *Metrics) planned(namespace, name string, pending int, at time.Time) {
	if m == nil {
		return
	}
	m.lastPlanned.WithLabelValues(namespace, name).Set(float64(at.Unix()))
	m.pending.WithLabelValues(namespace, name).Set(float64(pending))
}

// failed publishes a failed reconcile. warnings and last_applied keep
// describing the last apply that worked.
func (m *Metrics) failed(namespace, name string) {
	if m == nil {
		return
	}
	m.ready.WithLabelValues(namespace, name).Set(0)
}

func (m *Metrics) forget(namespace, name string) {
	if m == nil {
		return
	}
	for _, g := range []*prometheus.GaugeVec{m.ready, m.warnings, m.lastApplied, m.lastPlanned, m.pending} {
		g.DeleteLabelValues(namespace, name)
	}
}
