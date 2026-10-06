// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package secret reads credentials a resource references from a Secret in the
// resource's own namespace.
package secret

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Read returns one non-empty key of the Secret name in namespace. The
// namespace is always the referencing resource's, never one the reference
// supplies, so a resource cannot read another tenant's Secret.
func Read(ctx context.Context, c client.Reader, namespace, name, key string) ([]byte, error) {
	v, _, err := Get(ctx, c, namespace, name, key)
	return v, err
}

// Get is Read plus the Secret's resourceVersion, for a caller that must
// notice when the value changes but cannot read it back from the target.
func Get(ctx context.Context, c client.Reader, namespace, name, key string) ([]byte, string, error) {
	var s corev1.Secret
	ref := client.ObjectKey{Namespace: namespace, Name: name}
	if err := c.Get(ctx, ref, &s); err != nil {
		return nil, "", fmt.Errorf("reading secret %s: %w", ref, err)
	}
	raw, ok := s.Data[key]
	if !ok {
		return nil, "", fmt.Errorf("secret %s has no key %q (keys: %s)", ref, key, strings.Join(keys(&s), ", "))
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("secret %s key %q is empty", ref, key)
	}
	return raw, s.ResourceVersion, nil
}

func keys(s *corev1.Secret) []string {
	out := make([]string, 0, len(s.Data))
	for k := range s.Data {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
