// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
)

// FuzzInventory checks the set arithmetic pruning rests on, for inventories
// read back from status (unsorted, duplicated, anything a writer put there):
//
//   - stale is exactly the old keys the spec no longer wants: nothing the
//     spec wants is ever stale, and every old key is either wanted or stale;
//   - the new inventory, after a prune that left some stale keys behind,
//     holds every wanted key and every left-behind key, sorted and unique,
//     and nothing else.
func FuzzInventory(f *testing.F) {
	f.Add("a,b,c", "b,d", "c")
	f.Add("", "", "")
	f.Add("b,a,a,,b", "a", "b,zz")
	f.Add("x", "x,x", "x")
	f.Fuzz(func(t *testing.T, oldCSV, wantCSV, leftCSV string) {
		old := strings.Split(oldCSV, ",")
		desired := sortedSet(strings.Split(wantCSV, ","))
		stale := minus(old, desired)

		for _, k := range stale {
			if slices.Contains(desired, k) {
				t.Fatalf("wanted key %q is stale", k)
			}
		}
		for _, k := range old {
			if !slices.Contains(desired, k) && !slices.Contains(stale, k) {
				t.Fatalf("old key %q is neither wanted nor stale", k)
			}
		}
		if !slices.IsSorted(stale) || len(slices.Compact(slices.Clone(stale))) != len(stale) {
			t.Fatalf("stale %q is not sorted and unique", stale)
		}

		// The engine reports which stale keys survived; only stale keys can.
		var left []string
		for _, k := range strings.Split(leftCSV, ",") {
			if slices.Contains(stale, k) {
				left = append(left, k)
			}
		}
		next := union(desired, left)
		if !slices.IsSorted(next) || len(slices.Compact(slices.Clone(next))) != len(next) {
			t.Fatalf("inventory %q is not sorted and unique", next)
		}
		for _, k := range next {
			if !slices.Contains(desired, k) && !slices.Contains(left, k) {
				t.Fatalf("inventory gained %q", k)
			}
		}
		for _, k := range append(slices.Clone(desired), left...) {
			if !slices.Contains(next, k) {
				t.Fatalf("inventory lost %q", k)
			}
		}
	})
}
