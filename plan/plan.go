// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package plan models provisioning as a set of steps that can be printed
// before they are run: Describe gives a dry run, and Apply reports per-step
// outcomes.
//
// Steps run in the order they were added, unless some declare dependencies
// (Identified and Dependent): then they run in dependency order, and a step
// can consume a value an earlier step produced at apply time (SetOutput and
// Ref). A plan with no dependencies behaves exactly as an ordered list.
package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Step is one unit of provisioning work.
type Step interface {
	// Describe returns what the step will run, such as its SQL text.
	Describe() string

	// Apply performs the step.
	Apply(ctx context.Context) error

	// Rationale explains why the step exists. Printed by a dry run.
	Rationale() string

	// IsBestEffort reports whether a failure should be recorded as a warning
	// rather than aborting the plan.
	IsBestEffort() bool

	// Tolerates reports whether err is an expected, benign failure for this
	// step -- "role already exists" on a create, for instance.
	Tolerates(err error) bool
}

// Identified is a step other steps can depend on, or whose outputs they can
// read. IDs are unique within a plan.
type Identified interface {
	ID() string
}

// Dependent is a step that must run after the steps it names, and only if
// they succeeded.
type Dependent interface {
	DependsOn() []string
}

// Plan is a re-runnable set of steps.
type Plan struct {
	steps []Step
}

// Add appends steps to the plan.
func (p *Plan) Add(steps ...Step) {
	for _, s := range steps {
		if s == nil {
			continue
		}
		p.steps = append(p.steps, s)
	}
}

// Len returns the number of steps.
func (p *Plan) Len() int { return len(p.steps) }

// Steps returns the steps in the order Apply runs them. An invalid graph
// (see Order) falls back to the order they were added.
func (p *Plan) Steps() []Step {
	if ordered, err := p.Order(); err == nil {
		return ordered
	}
	return p.steps
}

// Order returns the steps in dependency order. Among steps whose
// dependencies are met, the one added first runs first, so a plan without
// dependencies keeps its insertion order. A duplicate ID, a dependency on an
// unknown ID, or a cycle is an error.
func (p *Plan) Order() ([]Step, error) {
	waiting, dependents, err := p.graph()
	if err != nil {
		return nil, err
	}
	out := make([]Step, 0, len(p.steps))
	done := make([]bool, len(p.steps))
	for len(out) < len(p.steps) {
		next := firstReady(done, waiting)
		if next < 0 {
			return nil, p.cycleError(done)
		}
		done[next] = true
		out = append(out, p.steps[next])
		for _, d := range dependents[next] {
			waiting[d]--
		}
	}
	return out, nil
}

// graph counts each step's unmet dependencies and lists each step's
// dependents, by index.
func (p *Plan) graph() (waiting []int, dependents [][]int, err error) {
	index := map[string]int{}
	for i, s := range p.steps {
		id := idOf(s)
		if id == "" {
			continue
		}
		if _, dup := index[id]; dup {
			return nil, nil, fmt.Errorf("plan: two steps have id %q", id)
		}
		index[id] = i
	}
	waiting = make([]int, len(p.steps))
	dependents = make([][]int, len(p.steps))
	for i, s := range p.steps {
		for _, dep := range depsOf(s) {
			j, ok := index[dep]
			if !ok {
				return nil, nil, fmt.Errorf("plan: %q depends on unknown step %q", firstLine(s.Describe()), dep)
			}
			waiting[i]++
			dependents[j] = append(dependents[j], i)
		}
	}
	return waiting, dependents, nil
}

// firstReady returns the earliest-added step with no unmet dependency, or -1.
func firstReady(done []bool, waiting []int) int {
	for i := range waiting {
		if !done[i] && waiting[i] == 0 {
			return i
		}
	}
	return -1
}

func (p *Plan) cycleError(done []bool) error {
	var stuck []string
	for i, s := range p.steps {
		if !done[i] {
			stuck = append(stuck, firstLine(s.Describe()))
		}
	}
	return fmt.Errorf("plan: dependency cycle among %q", stuck)
}

// Describe renders the whole plan as annotated text, suitable for review.
func (p *Plan) Describe() string {
	var b strings.Builder
	steps, err := p.Order()
	if err != nil {
		fmt.Fprintf(&b, "-- INVALID PLAN: %v\n\n", err)
		steps = p.steps
	}
	for i, s := range steps {
		if i > 0 {
			b.WriteString("\n")
		}
		if r := s.Rationale(); r != "" {
			for _, line := range strings.Split(r, "\n") {
				b.WriteString("-- ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
		if s.IsBestEffort() {
			b.WriteString("-- best-effort: failure is recorded as a warning, not fatal\n")
		}
		if deps := depsOf(s); len(deps) > 0 {
			fmt.Fprintf(&b, "-- after: %s\n", strings.Join(deps, ", "))
		}
		b.WriteString(s.Describe())
		b.WriteString("\n")
	}
	return b.String()
}

// Hash fingerprints the plan's steps in apply order, for
// status.appliedPlanHash.
func (p *Plan) Hash() string {
	var b strings.Builder
	for _, s := range p.Steps() {
		b.WriteString(s.Describe())
		b.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// Result reports what Apply did.
type Result struct {
	// Warnings holds one message per best-effort step that failed, and per
	// step skipped because a dependency failed.
	Warnings []string

	// Applied is the number of steps that ran without a fatal error.
	Applied int

	// Tolerated is the number of steps whose expected error was ignored.
	Tolerated int

	// Skipped is the number of steps not run because a dependency failed.
	Skipped int
}

// Apply runs every step in dependency order. A fatal error stops the plan
// and names the step; best-effort failures accumulate in Result.Warnings,
// and steps depending on a failed one are skipped with a warning of their
// own. An invalid graph runs nothing.
func (p *Plan) Apply(ctx context.Context) (Result, error) {
	var res Result
	steps, err := p.Order()
	if err != nil {
		return res, err
	}
	ctx = withOutputs(ctx)
	failed := map[string]bool{} // ids of steps that failed or were skipped
	for _, s := range steps {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if dep := firstFailed(s, failed); dep != "" {
			res.Skipped++
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s: skipped, %q did not succeed", firstLine(s.Describe()), dep))
			markFailed(s, failed)
			continue
		}
		err := s.Apply(ctx)
		switch {
		case err == nil:
			res.Applied++
		case s.Tolerates(err):
			res.Tolerated++
			res.Applied++
		case s.IsBestEffort():
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s: %v", firstLine(s.Describe()), err))
			markFailed(s, failed)
		default:
			return res, fmt.Errorf("applying %q: %w", firstLine(s.Describe()), err)
		}
	}
	return res, nil
}

func idOf(s Step) string {
	if i, ok := s.(Identified); ok {
		return i.ID()
	}
	return ""
}

func depsOf(s Step) []string {
	if d, ok := s.(Dependent); ok {
		return d.DependsOn()
	}
	return nil
}

func firstFailed(s Step, failed map[string]bool) string {
	for _, dep := range depsOf(s) {
		if failed[dep] {
			return dep
		}
	}
	return ""
}

func markFailed(s Step, failed map[string]bool) {
	if id := idOf(s); id != "" {
		failed[id] = true
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
