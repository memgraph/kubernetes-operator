/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// TestSystemUserSecrets pins the two Secrets of the system user: the password
// under one name the controller creates and reads, the hash under the other
// name the pods mount, so a pod never holds the password. Both are labelled
// like the cluster's other objects.
func TestSystemUserSecrets(t *testing.T) {
	cluster := minimalCluster()
	secret := func(name, key, value string) *corev1.Secret {
		return &corev1.Secret{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
				Labels:    expectedLabels("system-user"),
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: []byte(value)},
		}
	}

	want := secret(clusterName+"-system-user", "password", "s3cret")
	if diff := cmp.Diff(want, resources.SystemUserSecret(cluster, "s3cret")); diff != "" {
		t.Errorf("SystemUserSecret() mismatch (-want +got):\n%s", diff)
	}

	const hash = "bcrypt:$2a$10$hash"
	want = secret(clusterName+"-system-user-hash", "hash", hash)
	if diff := cmp.Diff(want, resources.SystemUserHashSecret(cluster, hash)); diff != "" {
		t.Errorf("SystemUserHashSecret() mismatch (-want +got):\n%s", diff)
	}
}
