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
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/settings"
)

// Where a role's flags reach its pods. The flags do not travel as container
// arguments: any argument is part of the pod template, so a change to one
// would bump the StatefulSet revision and roll every pod, which is the
// opposite of what a run-time flag should do. They travel as a gflags flag
// file in a per-role ConfigMap instead, mounted whole (never through subPath,
// which pins the file at pod start) so the kubelet refreshes it in place and
// any later start, a container restart included, reads the current flags.
// The container loads it with --flag-file ahead of the flags the operator
// pins on the command line, so a pinned flag wins any repeat.
const (
	// FlagFileKey is the file name the flag file is mounted as, which is the
	// key it sits under in the ConfigMap.
	FlagFileKey = "memgraph.flags"

	flagsVolumeName = "flags"
	// flagsMountPath is a sibling of /etc/memgraph rather than a path under
	// it: a ConfigMap mounted inside that directory would shadow the image's
	// own memgraph.conf and SSL directory.
	flagsMountPath = "/etc/memgraph-flags"
	flagFilePath   = flagsMountPath + "/" + FlagFileKey

	// FlagsRestartAnnotation is the pod-template annotation that carries a
	// hash of the role's startup-only flags. It is what turns a change to one
	// of them into a changed pod template, and so into a roll, while leaving
	// a change to a run-time flag — which is applied with SET DATABASE
	// SETTING and read from the file at the next start — out of the revision
	// entirely.
	FlagsRestartAnnotation = "memgraph.com/flags-restart-hash"

	flagsComponent = "flags"
)

// CoordinatorFlagsName and DataFlagsName are the names of the ConfigMaps
// holding each role's flag file.
func CoordinatorFlagsName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return CoordinatorName(cluster) + "-" + flagsComponent
}

// DataFlagsName is the name of the ConfigMap holding the data instances'
// flag file.
func DataFlagsName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return DataName(cluster) + "-" + flagsComponent
}

// CoordinatorFlags is every flag the coordinator pods run with from the flag
// file, by canonical gflags name: the operator's own defaults with the role's
// spec.flags merged over them. It is the view the controller diffs an
// instance's run-time settings against.
func CoordinatorFlags(cluster *memgraphcomv1alpha1.MemgraphCluster) map[string]string {
	return roleFlags(normalize(cluster.Spec).coordinatorRole)
}

// DataFlags is the same for the data instance pods.
func DataFlags(cluster *memgraphcomv1alpha1.MemgraphCluster) map[string]string {
	return roleFlags(normalize(cluster.Spec).dataRole)
}

// CoordinatorFlagsConfigMap builds the ConfigMap holding the coordinators'
// flag file.
func CoordinatorFlagsConfigMap(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.ConfigMap {
	spec := normalize(cluster.Spec)
	return flagsConfigMap(cluster, coordinatorComponent, CoordinatorFlagsName(cluster), spec.coordinatorRole)
}

// DataFlagsConfigMap builds the ConfigMap holding the data instances' flag
// file.
func DataFlagsConfigMap(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.ConfigMap {
	spec := normalize(cluster.Spec)
	return flagsConfigMap(cluster, dataComponent, DataFlagsName(cluster), spec.dataRole)
}

func flagsConfigMap(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component, name string,
	role normalizedRole,
) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: configMapKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels(cluster, component, nil),
		},
		Data: map[string]string{FlagFileKey: flagFile(roleFlags(role))},
	}
}

// defaultFlags are the flags the operator starts every instance with unless
// the role's spec.flags says otherwise: the HA chart's logging defaults. They
// live in the flag file rather than on the command line because a user may
// override them, and a command-line flag would beat the file; the command
// line carries only what a user may not override.
//
// also_log_to_stderr is written with an explicit value on purpose. A bare
// --flag is a boolean on the command line, but inside a flag file gflags
// reads it as a flag missing its value and ignores it.
func defaultFlags() map[string]string {
	return map[string]string{
		"log_level":          "TRACE",
		"also_log_to_stderr": "true",
		"log_retention_days": "35",
	}
}

// roleFlags merges the role's spec.flags over the operator's defaults, every
// key in its canonical gflags spelling so log-level and log_level cannot land
// as two entries. Admission rejects a map spelling one flag twice, so the
// merge order among a role's own keys does not matter; it is sorted anyway
// so the result is the same on every pass.
func roleFlags(role normalizedRole) map[string]string {
	flags := defaultFlags()
	for _, key := range slices.Sorted(maps.Keys(role.flags)) {
		flags[settings.Normalize(key)] = role.flags[key]
	}
	return flags
}

// flagFile renders flags as a gflags flag file: one --name=value line per
// flag, sorted by name. The order is for stable diffs only; gflags takes the
// last occurrence and the keys are unique.
func flagFile(flags map[string]string) string {
	var b strings.Builder
	for _, key := range slices.Sorted(maps.Keys(flags)) {
		b.WriteString("--")
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(flags[key])
		b.WriteString("\n")
	}
	return b.String()
}

// flagsRestartHash hashes the startup-only flags of the file, name and value,
// for the pod-template annotation. Run-time flags are left out: they reach a
// running instance without a restart and the file carries them for the next
// one, so a change to them must not change the template.
func flagsRestartHash(flags map[string]string) string {
	h := sha256.New()
	for _, key := range slices.Sorted(maps.Keys(flags)) {
		if settings.IsRuntime(key) {
			continue
		}
		h.Write([]byte(key))
		h.Write([]byte{'='})
		h.Write([]byte(flags[key]))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// flagsVolume is the ConfigMap volume a role's pods mount the flag file from.
func flagsVolume(name string) corev1.Volume {
	return corev1.Volume{
		Name: flagsVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			},
		},
	}
}
