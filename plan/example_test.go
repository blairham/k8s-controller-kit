// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"context"
	"fmt"

	"github.com/blairham/k8s-controller-kit/plan"
)

// A listener needs the ARN of a target group that does not exist yet. The
// listener is added first, but depends on the target group, so it runs
// second and reads the ARN the target group published.
func ExampleRef() {
	tgARN := plan.Ref{Step: "tg", Name: "arn"}

	var p plan.Plan
	p.Add(
		&plan.Op{
			Text: "CREATE LISTENER :443 -> " + tgARN.String(), Name: "listener", After: []string{"tg"},
			Do: func(ctx context.Context) error {
				arn, err := tgARN.Resolve(ctx)
				if err != nil {
					return err
				}
				fmt.Println("listener forwards to", arn)
				return nil
			},
		},
		&plan.Op{
			Text: "CREATE TARGET GROUP web", Name: "tg",
			Do: func(ctx context.Context) error {
				plan.SetOutput(ctx, "tg", "arn", "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/1")
				return nil
			},
		},
	)

	fmt.Print(p.Describe())
	if _, err := p.Apply(context.Background()); err != nil {
		fmt.Println(err)
	}
	// Output:
	// CREATE TARGET GROUP web
	//
	// -- after: tg
	// CREATE LISTENER :443 -> ${tg.arn}
	// listener forwards to arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/1
}
