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

package resources

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// The operator authenticates to Memgraph as its internal system user,
// memgraph_internal: a user no query can create, change or drop, which
// Memgraph accepts on data instances and coordinators whether or not other
// users or SSO are configured. Memgraph proves it against a bcrypt hash it
// reads from the file --system-user-internal-hash-file names (Memgraph 3.15
// and later).
//
// The password lives in one Secret the operator generates and alone reads;
// the hash in a second one, the only one the pods mount, so the password never
// reaches a Memgraph pod. The flag that points Memgraph at the hash travels in
// the flag file rather than on the command line: a release without the flag
// ignores an unknown name in a flag file but refuses one on the command line,
// so a cluster on an older image keeps starting and the operator falls back to
// connecting without credentials, as it always has.
const (
	// SystemUserPasswordKey is the key of the password in the generated
	// Secret, and SystemUserHashKey the key of the hash ("bcrypt:$2a$...") in
	// the Secret the pods mount.
	SystemUserPasswordKey = "password"
	SystemUserHashKey     = "hash"

	systemUserComponent  = "system-user"
	systemUserVolumeName = "system-user"
	// systemUserMountPath is under /etc/memgraph like the TLS mounts, and a
	// directory of its own so the mount shadows nothing of the image's.
	systemUserMountPath = "/etc/memgraph/system-user"
	// systemUserHashFlag is the flag naming the hash file, in its canonical
	// spelling; admission denies it in spec.flags.
	systemUserHashFlag = "system_user_internal_hash_file"

	secretKind = "Secret"
)

// SystemUserSecretName is the Secret holding the system user's password.
func SystemUserSecretName(cluster *memgraphcomv1alpha1.MemgraphDBCluster) string {
	return cluster.Name + "-" + systemUserComponent
}

// SystemUserHashSecretName is the Secret holding its hash, mounted into every
// pod of both roles.
func SystemUserHashSecretName(cluster *memgraphcomv1alpha1.MemgraphDBCluster) string {
	return SystemUserSecretName(cluster) + "-hash"
}

// SystemUserSecret is the Secret holding the system user's password. The
// controller creates it once with a generated password and never rewrites it.
func SystemUserSecret(cluster *memgraphcomv1alpha1.MemgraphDBCluster, password string) *corev1.Secret {
	return systemUserSecret(cluster, SystemUserSecretName(cluster), SystemUserPasswordKey, password)
}

// SystemUserHashSecret is the Secret holding the hash of that password, in
// the "bcrypt:<hash>" form Memgraph reads.
func SystemUserHashSecret(cluster *memgraphcomv1alpha1.MemgraphDBCluster, hash string) *corev1.Secret {
	return systemUserSecret(cluster, SystemUserHashSecretName(cluster), SystemUserHashKey, hash)
}

func systemUserSecret(cluster *memgraphcomv1alpha1.MemgraphDBCluster, name, key, value string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: secretKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels(cluster, systemUserComponent, nil),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{key: []byte(value)},
	}
}

// systemUserVolume mounts the hash Secret. It is required, not optional: a pod
// whose Secret is missing waits in ContainerCreating rather than starting
// without the system user and locking the operator out.
func systemUserVolume(cluster *memgraphcomv1alpha1.MemgraphDBCluster) corev1.Volume {
	return corev1.Volume{
		Name: systemUserVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: SystemUserHashSecretName(cluster),
				Items:      []corev1.KeyToPath{{Key: SystemUserHashKey, Path: SystemUserHashKey}},
			},
		},
	}
}

func systemUserVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: systemUserVolumeName, MountPath: systemUserMountPath, ReadOnly: true}
}
