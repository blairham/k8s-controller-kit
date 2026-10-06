// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/blairham/k8s-controller-kit/plan"
)

// recorder builds Ops that log their own execution.
type recorder struct{ ran []string }

func (r *recorder) op(name string, after ...string) *plan.Op {
	return &plan.Op{
		Text: "RUN " + name, Name: name, After: after,
		Do: func(context.Context) error { r.ran = append(r.ran, name); return nil },
	}
}

func TestDependenciesReorderStably(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var p plan.Plan
	// Added listener-first; it must run after both target groups and the LB,
	// while the independent steps keep their relative order.
	p.Add(r.op("listener", "lb", "tg-b"), r.op("tg-a"), r.op("lb", "tg-a"), r.op("tg-b"), r.op("tags"))
	if _, err := p.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tg-a", "lb", "tg-b", "listener", "tags"}; !slices.Equal(r.ran, want) {
		t.Errorf("ran %v, want %v", r.ran, want)
	}
	texts := make([]string, 0, p.Len())
	for _, s := range p.Steps() {
		texts = append(texts, s.Describe())
	}
	if want := []string{"RUN tg-a", "RUN lb", "RUN tg-b", "RUN listener", "RUN tags"}; !slices.Equal(texts, want) {
		t.Errorf("Steps() = %v, want apply order %v", texts, want)
	}
	if !strings.Contains(p.Describe(), "-- after: lb, tg-b\nRUN listener") {
		t.Errorf("Describe does not show dependencies:\n%s", p.Describe())
	}
}

// A plan without dependencies must keep its order, text and hash, so moving
// an existing controller onto this package changes nothing it reports.
func TestPlainPlanIsUnchanged(t *testing.T) {
	t.Parallel()
	var p plan.Plan
	p.Add(step{text: "B"}, step{text: "A"}, step{text: "C"})
	h := sha256.New()
	for _, s := range []string{"B", "A", "C"} {
		h.Write([]byte(s + "\x00"))
	}
	if want := hex.EncodeToString(h.Sum(nil))[:16]; p.Hash() != want {
		t.Errorf("Hash = %s, want the insertion-order hash %s", p.Hash(), want)
	}
	if p.Describe() != "B\n\nA\n\nC\n" {
		t.Errorf("Describe = %q", p.Describe())
	}
}

func TestOutputsFlowThroughRefs(t *testing.T) {
	t.Parallel()
	arn := plan.Ref{Step: "tg", Name: "arn"}
	var got string
	var p plan.Plan
	p.Add(
		&plan.Op{
			Text: "CREATE LISTENER -> " + arn.String(), Name: "listener", After: []string{"tg"},
			Do: func(ctx context.Context) error {
				v, err := arn.Resolve(ctx)
				got = v
				return err
			},
		},
		&plan.Op{
			Text: "CREATE TARGET GROUP", Name: "tg",
			Do: func(ctx context.Context) error {
				plan.SetOutput(ctx, "tg", "arn", "arn:aws:elasticloadbalancing:tg/1")
				return nil
			},
		},
	)
	if !strings.Contains(p.Describe(), "CREATE LISTENER -> ${tg.arn}") {
		t.Errorf("unresolved ref not shown in plan text:\n%s", p.Describe())
	}
	if _, err := p.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "arn:aws:elasticloadbalancing:tg/1" {
		t.Errorf("listener saw %q", got)
	}
}

func TestRefWithoutTheOutputFails(t *testing.T) {
	t.Parallel()
	var p plan.Plan
	p.Add(&plan.Op{Text: "USE", Do: func(ctx context.Context) error {
		_, err := plan.Ref{Step: "nope", Name: "x"}.Resolve(ctx)
		return err
	}})
	if _, err := p.Apply(
		context.Background(),
	); err == nil ||
		!strings.Contains(err.Error(), "${nope.x} was not published") {
		t.Errorf("err = %v", err)
	}
	if _, err := (plan.Ref{Step: "a", Name: "b"}).Resolve(context.Background()); err == nil {
		t.Error("Resolve outside Apply succeeded")
	}
}

func TestFailedDependencySkipsDependentsTransitively(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var p plan.Plan
	p.Add(
		&plan.Op{
			Text:       "RUN tg",
			Name:       "tg",
			BestEffort: true,
			Do:         func(context.Context) error { return errors.New("quota") },
		},
		r.op("lb", "tg"),
		r.op("listener", "lb"),
		r.op("unrelated"),
	)
	res, err := p.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"unrelated"}) {
		t.Errorf("ran %v; dependents of a failed step must not run", r.ran)
	}
	if res.Skipped != 2 || len(res.Warnings) != 3 || res.Applied != 1 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(res.Warnings[2], `RUN listener: skipped, "lb" did not succeed`) {
		t.Errorf("warnings = %q", res.Warnings)
	}
}

func TestToleratedDependencyCountsAsSuccess(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	exists := errors.New("exists")
	var p plan.Plan
	p.Add(
		&plan.Op{
			Text: "CREATE", Name: "a",
			Tolerate: func(err error) bool { return errors.Is(err, exists) },
			Do:       func(context.Context) error { return exists },
		},
		r.op("b", "a"),
	)
	if _, err := p.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"b"}) {
		t.Errorf("ran %v", r.ran)
	}
}

func TestInvalidGraphsRunNothing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		build func(*recorder) []plan.Step
		want  string
	}{
		"cycle": {func(r *recorder) []plan.Step {
			return []plan.Step{r.op("a", "b"), r.op("b", "a"), r.op("c")}
		}, "dependency cycle"},
		"unknown": {func(r *recorder) []plan.Step {
			return []plan.Step{r.op("a", "ghost")}
		}, `depends on unknown step "ghost"`},
		"duplicate": {func(r *recorder) []plan.Step {
			return []plan.Step{r.op("a"), r.op("a")}
		}, `two steps have id "a"`},
	} {
		r := &recorder{}
		var p plan.Plan
		p.Add(tc.build(r)...)
		if _, err := p.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
		if len(r.ran) != 0 {
			t.Errorf("%s: ran %v from an invalid plan", name, r.ran)
		}
		if !strings.HasPrefix(p.Describe(), "-- INVALID PLAN: ") {
			t.Errorf("%s: Describe does not flag the plan:\n%s", name, p.Describe())
		}
	}
}
