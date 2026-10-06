// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package secret_test

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/blairham/k8s-controller-kit/secret"
)

func TestRead(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithObjects(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "creds"},
			Data:       map[string][]byte{"password": []byte("pw"), "empty": nil, "user": []byte("u")},
		},
	).Build()
	ctx := context.Background()

	if got, err := secret.Read(ctx, c, "a", "creds", "password"); err != nil || string(got) != "pw" {
		t.Errorf("Read = %q, %v", got, err)
	}
	for _, tc := range []struct{ ns, key, want string }{
		{"b", "password", "not found"}, // another namespace is never consulted
		{"a", "missing", `no key "missing" (keys: empty, password, user)`},
		{"a", "empty", "is empty"},
	} {
		if _, err := secret.Read(ctx, c, tc.ns, "creds", tc.key); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Read(%s, %s) err = %v, want %q", tc.ns, tc.key, err, tc.want)
		}
	}
}
