// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/blairham/k8s-controller-kit/plan"
)

// FuzzPlan builds a plan from bytes -- ids that may repeat or be missing,
// dependencies that may be unknown or cyclic, steps that may fail -- and
// checks, whatever the graph:
//
//   - Order either fails, or returns every step exactly once with each
//     dependency before its dependent; with no dependencies at all it is the
//     insertion order;
//   - Apply runs nothing when Order fails, and otherwise runs steps in
//     Order's order, never a step whose dependency failed or was skipped;
//   - Describe and Hash never panic, and Hash does not depend on how many
//     times it is called.
func FuzzPlan(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01, 0x00, 0x00, 0x00, 0x02, 0x00})       // no dependencies: insertion order
	f.Add([]byte{0x11, 0x00, 0x22, 0x10, 0x33, 0x21})       // a chain
	f.Add([]byte{0x11, 0x20, 0x22, 0x10})                   // a cycle
	f.Add([]byte{0x11, 0x00, 0x11, 0x00})                   // a duplicate id
	f.Add([]byte{0x11, 0x70, 0x02, 0x00})                   // an unknown dependency
	f.Add([]byte{0x91, 0x00, 0x22, 0x10, 0x33, 0x20, 0x04}) // a failing root
	f.Fuzz(func(t *testing.T, data []byte) {
		type spec struct {
			id   string
			deps []string
			fail bool
		}
		var specs []spec
		for i := 0; i+1 < len(data) && len(specs) < 16; i += 2 {
			b, d := data[i], data[i+1]
			s := spec{fail: b&0x80 != 0}
			if n := b & 0x0f; n != 0 {
				s.id = fmt.Sprintf("s%d", n%8) // few names: duplicates happen
			}
			for _, dep := range []byte{d >> 4, d & 0x0f} {
				if dep != 0 {
					s.deps = append(s.deps, fmt.Sprintf("s%d", dep%9)) // s8 never exists
				}
			}
			specs = append(specs, s)
		}

		var ran []int
		var p plan.Plan
		steps := make([]plan.Step, len(specs))
		for i, s := range specs {
			op := &plan.Op{
				Text: fmt.Sprintf("step %d", i), Name: s.id, After: s.deps, BestEffort: true,
				Do: func(context.Context) error {
					ran = append(ran, i)
					if s.fail {
						return errors.New("refused")
					}
					return nil
				},
			}
			steps[i] = op
			p.Add(op)
		}
		_ = p.Describe()
		if first, again := p.Hash(), p.Hash(); first != again {
			t.Fatalf("Hash is not stable: %s then %s", first, again)
		}

		ordered, orderErr := p.Order()
		res, applyErr := p.Apply(context.Background())
		if orderErr != nil {
			if applyErr == nil || len(ran) != 0 {
				t.Fatalf("invalid plan (%v) ran %v, apply err %v", orderErr, ran, applyErr)
			}
			return
		}
		if applyErr != nil {
			t.Fatalf("valid plan failed to apply: %v", applyErr)
		}

		pos := map[plan.Step]int{}
		for i, s := range ordered {
			if _, dup := pos[s]; dup {
				t.Fatalf("step %q ordered twice", s.Describe())
			}
			pos[s] = i
		}
		if len(pos) != len(steps) {
			t.Fatalf("ordered %d of %d steps", len(pos), len(steps))
		}
		byID := map[string]plan.Step{}
		anyDeps := false
		for i, s := range specs {
			if s.id != "" {
				byID[s.id] = steps[i]
			}
			anyDeps = anyDeps || len(s.deps) > 0
		}
		for i, s := range specs {
			for _, d := range s.deps {
				if pos[byID[d]] >= pos[steps[i]] {
					t.Fatalf("step %d ordered before its dependency %s", i, d)
				}
			}
		}
		if !anyDeps {
			for i, s := range ordered {
				if s != steps[i] {
					t.Fatal("a plan without dependencies was reordered")
				}
			}
		}

		// Apply ran in Order's order, and never past a failed dependency.
		index := map[plan.Step]int{}
		for i, st := range steps {
			index[st] = i
		}
		bad := map[string]bool{}
		next, failed := 0, 0
		for _, st := range ordered {
			i := index[st]
			blocked := false
			for _, d := range specs[i].deps {
				blocked = blocked || bad[d]
			}
			if blocked || specs[i].fail {
				if specs[i].id != "" {
					bad[specs[i].id] = true
				}
			}
			if blocked {
				continue
			}
			if next >= len(ran) || ran[next] != i {
				t.Fatalf("expected step %d to run next (position %d), ran %v", i, next, ran)
			}
			next++
			if specs[i].fail {
				failed++
			}
		}
		if next != len(ran) {
			t.Fatalf("ran %v, expected %d steps", ran, next)
		}
		if res.Applied+res.Skipped+failed != len(steps) || res.Skipped+failed != len(res.Warnings) {
			t.Fatalf("result %+v does not account for %d steps (%d failed)", res, len(steps), failed)
		}
	})
}
