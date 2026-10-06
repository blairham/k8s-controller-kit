// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"context"
	"fmt"
	"sync"
)

// outputs holds the values steps publish during one Apply, keyed by step id
// and then by name.
type outputs struct {
	m  map[string]map[string]string
	mu sync.Mutex
}

type outputsKey struct{}

func withOutputs(ctx context.Context) context.Context {
	return context.WithValue(ctx, outputsKey{}, &outputs{m: map[string]map[string]string{}})
}

func outputsFrom(ctx context.Context) *outputs {
	o, ok := ctx.Value(outputsKey{}).(*outputs)
	if !ok {
		return nil
	}
	return o
}

// SetOutput publishes a value from the step with id stepID, for steps that
// depend on it -- the ARN or generated id of something it just created. Call
// it from Step.Apply, with the context Apply received; outside Apply it does
// nothing.
func SetOutput(ctx context.Context, stepID, name, value string) {
	o := outputsFrom(ctx)
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m[stepID] == nil {
		o.m[stepID] = map[string]string{}
	}
	o.m[stepID][name] = value
}

// Ref names a value a step publishes, to be read when a later step applies.
// Before apply it renders as ${step.name}, so plan text shows where the value
// comes from. A step holding a Ref should list Ref.Step in DependsOn.
type Ref struct {
	Step string
	Name string
}

// String renders the reference for plan text.
func (r Ref) String() string { return "${" + r.Step + "." + r.Name + "}" }

// Resolve returns the referenced value. It fails if the step did not publish
// it, which also catches a Ref whose step is not among the dependencies.
func (r Ref) Resolve(ctx context.Context) (string, error) {
	o := outputsFrom(ctx)
	if o == nil {
		return "", fmt.Errorf("resolving %s outside Apply", r)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.m[r.Step][r.Name]
	if !ok {
		return "", fmt.Errorf("%s was not published (is %q in DependsOn, and did it run?)", r, r.Step)
	}
	return v, nil
}
