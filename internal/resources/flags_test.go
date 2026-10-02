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

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// The flag names and values the cases in this package share.
const (
	logLevelFlag    = "log-level"
	memoryLimitFlag = "memory-limit"
	snapshotOnExit  = "storage-snapshot-on-exit"

	flagOn    memgraphcomv1alpha1.FlagValue = "true"
	flagOff   memgraphcomv1alpha1.FlagValue = "false"
	infoLevel memgraphcomv1alpha1.FlagValue = "INFO"
)

// defaultFlagFile is the flag file of a role without spec.flags: the HA
// chart's logging defaults, every flag in gflags' underscore spelling with an
// explicit value, sorted by name.
const defaultFlagFile = "--also_log_to_stderr=true\n--log_level=TRACE\n--log_retention_days=35\n"

// TestFlagsConfigMapDefaults pins the two ConfigMaps a cluster without
// spec.flags gets: one per role, labelled like the role's other objects so the
// label-scoped ConfigMap cache sees them, holding the default flag file.
func TestFlagsConfigMapDefaults(t *testing.T) {
	cluster := minimalCluster()
	for _, tc := range []struct {
		component string
		got       *corev1.ConfigMap
	}{
		{coordinatorComponent, resources.CoordinatorFlagsConfigMap(cluster)},
		{dataComponent, resources.DataFlagsConfigMap(cluster)},
	} {
		t.Run(tc.component, func(t *testing.T) {
			want := &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: configMapKind},
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName + "-" + tc.component + "-flags",
					Namespace: testNamespace,
					Labels:    expectedLabels(tc.component),
				},
				Data: map[string]string{"memgraph.flags": defaultFlagFile},
			}
			if diff := cmp.Diff(want, tc.got); diff != "" {
				t.Errorf("flags ConfigMap mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFlagsConfigMapMergesTheRole asserts the role's spec.flags land in its
// file and nowhere else: merged over the defaults with the user's value
// winning, every key in the underscore spelling whatever the user wrote, an
// empty value kept as --flag=, and the lines sorted. The other role's file is
// untouched.
func TestFlagsConfigMapMergesTheRole(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Flags = memgraphcomv1alpha1.FlagsSpec{
		Data: map[string]memgraphcomv1alpha1.FlagValue{
			logLevelFlag:                infoLevel,
			snapshotOnExit:              flagOff,
			"storage_snapshot_interval": "",
			memoryLimitFlag:             "2048",
		},
	}

	wantData := "--also_log_to_stderr=true\n" +
		"--log_level=INFO\n" +
		"--log_retention_days=35\n" +
		"--memory_limit=2048\n" +
		"--storage_snapshot_interval=\n" +
		"--storage_snapshot_on_exit=false\n"
	if got := resources.DataFlagsConfigMap(cluster).Data[resources.FlagFileKey]; got != wantData {
		t.Errorf("data flag file:\n%s\nwant:\n%s", got, wantData)
	}
	if got := resources.CoordinatorFlagsConfigMap(cluster).Data[resources.FlagFileKey]; got != defaultFlagFile {
		t.Errorf("coordinator flag file:\n%s\nwant the defaults, the data flags are not theirs", got)
	}

	// The same view, as the controller diffs an instance's settings against it.
	wantFlags := map[string]string{
		"also_log_to_stderr":        string(flagOn),
		"log_level":                 string(infoLevel),
		"log_retention_days":        "35",
		"memory_limit":              "2048",
		"storage_snapshot_interval": "",
		"storage_snapshot_on_exit":  string(flagOff),
	}
	if diff := cmp.Diff(wantFlags, resources.DataFlags(cluster)); diff != "" {
		t.Errorf("DataFlags mismatch (-want +got):\n%s", diff)
	}
}

// TestFlagsRestartHashFollowsStartupFlagsOnly is the contract the whole
// feature rests on: a change to a run-time flag leaves the pod template as it
// was, so nothing rolls, while a change to a startup-only flag changes the
// template, so the roll carries it. Both are read off the template annotation
// the StatefulSet revision hashes.
func TestFlagsRestartHashFollowsStartupFlagsOnly(t *testing.T) {
	hashOf := func(flags map[string]memgraphcomv1alpha1.FlagValue) string {
		cluster := minimalCluster()
		cluster.Spec.Flags = memgraphcomv1alpha1.FlagsSpec{Data: flags}
		return dataStatefulSet(cluster).Spec.Template.Annotations[resources.FlagsRestartAnnotation]
	}

	base := hashOf(nil)
	if base != defaultRestartHash {
		t.Errorf("default restart hash = %q, want %q", base, defaultRestartHash)
	}

	for name, flags := range map[string]map[string]memgraphcomv1alpha1.FlagValue{
		"a run-time flag changed":               {logLevelFlag: infoLevel},
		"a run-time flag in the other spelling": {"log_level": infoLevel},
		"a run-time flag added":                 {"query-execution-timeout-sec": "10"},
		"a run-time default restated":           {"also-log-to-stderr": flagOn},
	} {
		if got := hashOf(flags); got != base {
			t.Errorf("%s: restart hash changed to %q, so the pods would roll for a run-time flag", name, got)
		}
	}

	seen := map[string]string{"": base}
	for name, flags := range map[string]map[string]memgraphcomv1alpha1.FlagValue{
		"a startup-only flag added":                 {snapshotOnExit: flagOff},
		"a startup-only flag changed":               {snapshotOnExit: flagOn},
		"a startup-only default overridden":         {"log-retention-days": "7"},
		"a startup-only flag beside a run-time one": {memoryLimitFlag: "2048", logLevelFlag: infoLevel},
	} {
		got := hashOf(flags)
		for other, hash := range seen {
			if got == hash {
				t.Errorf("%s: restart hash %q equals the one for %q, so the pods would not roll", name, got, other)
			}
		}
		seen[name] = got
	}

	// The coordinators' hash follows their own flags, not the data instances'.
	cluster := minimalCluster()
	cluster.Spec.Flags = memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{memoryLimitFlag: "2048"}}
	if got := coordinatorStatefulSet(cluster).Spec.Template.Annotations[resources.FlagsRestartAnnotation]; got != base {
		t.Errorf("coordinator restart hash = %q after a data flag change, want %q", got, base)
	}
}
