// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package plan

import "context"

// Op is a ready-made Step for engines that do not need their own type.
type Op struct {
	// Tolerate, when set, reports an expected, benign error.
	Tolerate func(error) bool

	// Do performs the step. Publish outputs with SetOutput(ctx, Name, ...).
	Do func(ctx context.Context) error

	// Text is what Describe returns.
	Text string

	// Why is the Rationale, printed above Text by a dry run.
	Why string

	// Name is the step's ID, needed only if other steps depend on it or
	// read its outputs.
	Name string

	// After lists the IDs of the steps this one depends on.
	After []string

	// BestEffort records a failure as a warning instead of stopping the plan.
	BestEffort bool
}

var (
	_ Step       = (*Op)(nil)
	_ Identified = (*Op)(nil)
	_ Dependent  = (*Op)(nil)
)

// Describe implements Step.
func (o *Op) Describe() string { return o.Text }

// Rationale implements Step.
func (o *Op) Rationale() string { return o.Why }

// IsBestEffort implements Step.
func (o *Op) IsBestEffort() bool { return o.BestEffort }

// Tolerates implements Step.
func (o *Op) Tolerates(err error) bool { return o.Tolerate != nil && o.Tolerate(err) }

// Apply implements Step.
func (o *Op) Apply(ctx context.Context) error { return o.Do(ctx) }

// ID implements Identified.
func (o *Op) ID() string { return o.Name }

// DependsOn implements Dependent.
func (o *Op) DependsOn() []string { return o.After }
