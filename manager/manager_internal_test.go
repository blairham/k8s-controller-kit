// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"slices"
	"testing"
)

func TestSelectControllers(t *testing.T) {
	t.Parallel()
	known := map[string]Controller{"a": {}, "b": {}}
	for _, tc := range []struct {
		flag, err string
		want      []string
	}{
		{flag: "a,b", want: []string{"a", "b"}},
		{flag: " b , a,b,", want: []string{"b", "a"}},
		{flag: "a,typo", err: "unknown controllers: typo"},
		{flag: " , ", err: "--controllers selects no controller"},
	} {
		got, err := selectControllers(known, tc.flag)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("%q: err = %v, want %q", tc.flag, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%q = %v, %v; want %v", tc.flag, got, err, tc.want)
		}
	}
}
