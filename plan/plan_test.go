// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blairham/k8s-controller-kit/plan"
)

var errBoom = errors.New("boom")

type step struct {
	err        error
	ran        *int
	text       string
	why        string
	bestEffort bool
	tolerate   bool
}

func (s step) Describe() string   { return s.text }
func (s step) Rationale() string  { return s.why }
func (s step) IsBestEffort() bool { return s.bestEffort }
func (s step) Tolerates(err error) bool {
	return s.tolerate && errors.Is(err, errBoom)
}

func (s step) Apply(context.Context) error {
	*s.ran++
	return s.err
}

func TestApplyOutcomes(t *testing.T) {
	t.Parallel()
	var ran int
	var p plan.Plan
	p.Add(
		step{text: "ok", ran: &ran},
		nil, // dropped
		step{text: "tolerated", err: errBoom, tolerate: true, ran: &ran},
		step{text: "soft\nsecond line", err: errBoom, bestEffort: true, ran: &ran},
	)
	if p.Len() != 3 {
		t.Fatalf("Len = %d, want nil steps dropped", p.Len())
	}
	res, err := p.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 2 || res.Tolerated != 1 || len(res.Warnings) != 1 || ran != 3 {
		t.Errorf("result = %+v, ran %d", res, ran)
	}
	if res.Warnings[0] != "soft ...: boom" {
		t.Errorf("warning = %q", res.Warnings[0])
	}
}

func TestFatalStopsThePlan(t *testing.T) {
	t.Parallel()
	var ran int
	var p plan.Plan
	p.Add(step{text: "fatal", err: errBoom, ran: &ran}, step{text: "after", ran: &ran})
	_, err := p.Apply(context.Background())
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), `"fatal"`) {
		t.Errorf("err = %v", err)
	}
	if ran != 1 {
		t.Errorf("ran %d steps, want the plan stopped at the fatal one", ran)
	}
}

func TestDescribeAndHash(t *testing.T) {
	t.Parallel()
	var a, b plan.Plan
	a.Add(step{text: "CREATE x", why: "needed\nbecause"}, step{text: "GRANT", bestEffort: true})
	b.Add(step{text: "CREATE x"})
	want := "-- needed\n-- because\nCREATE x\n\n-- best-effort: failure is recorded as a warning, not fatal\nGRANT\n"
	if got := a.Describe(); got != want {
		t.Errorf("Describe =\n%s\nwant\n%s", got, want)
	}
	if a.Hash() == b.Hash() || len(a.Hash()) != 16 {
		t.Errorf("hashes %q %q", a.Hash(), b.Hash())
	}
}
