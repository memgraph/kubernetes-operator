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

package volumes

import (
	"fmt"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	data0   = "lib-storage-cluster-data-0"
	data1   = "lib-storage-cluster-data-1"
	sts     = "cluster-data"
	lib     = "lib-storage"
	dumps   = "core-dumps"
	pending = "FileSystemResizePending"
)

var resizedAt = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func size(s string) resource.Quantity { return resource.MustParse(s) }

func sizes(lib, dumps string) map[string]resource.Quantity {
	return map[string]resource.Quantity{"lib-storage": size(lib), "core-dumps": size(dumps)}
}

// claim is a bound claim of the given template and ordinal whose filesystem
// holds what it asks for.
func claim(template string, ordinal int32, request string) Claim {
	return Claim{
		Name:     fmt.Sprintf("%s-%s-%d", template, sts, ordinal),
		Template: template,
		Ordinal:  ordinal,
		Bound:    true,
		Request:  size(request),
		Capacity: size(request),
	}
}

func growing(c Claim, capacity string) Claim {
	c.Capacity = size(capacity)
	return c
}

func resizePending(c Claim, capacity string) Claim {
	c = growing(c, capacity)
	c.ResizePending = true
	return c
}

func pods(ordinals ...int32) []Pod {
	out := make([]Pod, 0, len(ordinals))
	for _, ordinal := range ordinals {
		out = append(out, Pod{Name: fmt.Sprintf("%s-%d", sts, ordinal), Ordinal: ordinal})
	}
	return out
}

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		name string
		role Role
		want Decision
	}{
		{
			name: "nothing to do when every claim and the template hold the desired size",
			role: Role{
				StatefulSet: sts, Desired: sizes("1Gi", "1Gi"), Live: sizes("1Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "1Gi"), claim(dumps, 0, "1Gi")},
				Pods:   pods(0),
			},
		},
		{
			name: "a grown size patches every claim of its template and holds the recreate back",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("1Gi", "1Gi"),
				Claims: []Claim{claim(lib, 1, "1Gi"), claim(lib, 0, "1Gi"), claim(dumps, 0, "1Gi")},
				Pods:   pods(0, 1),
			},
			want: Decision{
				Patches: []Patch{
					{Claim: data0, Size: size("10Gi")},
					{Claim: data1, Size: size("10Gi")},
				},
				Growing: []string{data0 + " (pod cluster-data-0)", data1 + " (pod cluster-data-1)"},
			},
		},
		{
			name: "a claim retained beyond the running pods is patched too",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "10Gi"), claim(lib, 4, "1Gi")},
				Pods:   pods(0),
			},
			want: Decision{
				Patches: []Patch{{Claim: "lib-storage-cluster-data-4", Size: size("10Gi")}},
				Growing: []string{"lib-storage-cluster-data-4"},
			},
		},
		{
			name: "once every claim asks for the size the StatefulSet is recreated, a quantity in other units being no difference",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1024Mi"), Live: sizes("1Gi", "1Gi"),
				Claims: []Claim{growing(claim(lib, 0, "10Gi"), "1Gi"), claim(dumps, 0, "1Gi")},
				Pods:   pods(0),
			},
			want: Decision{Recreate: true, Growing: []string{data0 + " (pod cluster-data-0)"}},
		},
		{
			name: "a live template larger than the spec is not recreated smaller",
			role: Role{
				StatefulSet: sts, Desired: sizes("1Gi", "1Gi"), Live: sizes("2Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "2Gi")},
				Pods:   pods(0),
			},
		},
		{
			name: "a StatefulSet that does not exist is created from the spec, not recreated",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "10Gi")},
				Pods:   pods(0),
			},
		},
		{
			name: "a claim already larger than the spec, grown by hand, is left alone",
			role: Role{
				StatefulSet: sts, Desired: sizes("1Gi", "1Gi"), Live: sizes("1Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "20Gi")},
				Pods:   pods(0),
			},
		},
		{
			name: "an unbound claim is neither patched nor waited on",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("1Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "10Gi"), func() Claim {
					c := claim(lib, 1, "1Gi")
					c.Bound = false
					return c
				}()},
				Pods: pods(0),
			},
			want: Decision{Recreate: true},
		},
		{
			name: "a claim of a template the role does not have is ignored",
			role: Role{
				StatefulSet: sts, Desired: map[string]resource.Quantity{lib: size("1Gi")},
				Live:   map[string]resource.Quantity{lib: size("1Gi")},
				Claims: []Claim{claim("core-dumps", 0, "1Gi")},
				Pods:   pods(0),
			},
		},
		{
			name: "a mounted claim whose filesystem is still to grow is waited on, naming its pod",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{resizePending(claim(lib, 0, "10Gi"), "1Gi"), resizePending(claim(dumps, 0, "1Gi"), "512Mi"),
					resizePending(claim(lib, 1, "10Gi"), "1Gi")},
				Pods: pods(0, 1),
			},
			want: Decision{Growing: []string{
				"core-dumps-cluster-data-0 (pod cluster-data-0)",
				data0 + " (pod cluster-data-0)", data1 + " (pod cluster-data-1)",
			}},
		},
		{
			name: "a mounted claim whose volume is still growing is waited on, naming its pod",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{growing(claim(lib, 0, "10Gi"), "1Gi")},
				Pods:   pods(0),
			},
			want: Decision{Growing: []string{data0 + " (pod cluster-data-0)"}},
		},
		{
			name: "a claim nothing mounts is done once only its filesystem is left to grow",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{claim(lib, 0, "10Gi"), resizePending(claim(lib, 3, "10Gi"), "1Gi")},
				Pods:   pods(0),
			},
		},
		{
			name: "a claim nothing mounts is waited on while its volume grows",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{growing(claim(lib, 3, "10Gi"), "1Gi")},
				Pods:   pods(0),
			},
			want: Decision{Growing: []string{"lib-storage-cluster-data-3"}},
		},
		{
			name: "a resize the driver keeps failing is waited on with its error",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{func() Claim {
					c := growing(claim(lib, 0, "10Gi"), "1Gi")
					c.ResizeError = "Change in disk property of VM of size 'Standard_A2_v2' is not supported."
					return c
				}()},
				Pods: pods(0),
			},
			want: Decision{
				Growing: []string{data0 + " (pod cluster-data-0)"},
				Errors:  []string{data0 + ": Change in disk property of VM of size 'Standard_A2_v2' is not supported."},
			},
		},
		{
			name: "a resize the provider gave up on is reported",
			role: Role{
				StatefulSet: sts, Desired: sizes("10Gi", "1Gi"), Live: sizes("10Gi", "1Gi"),
				Claims: []Claim{func() Claim {
					c := growing(claim(lib, 0, "10Gi"), "1Gi")
					c.Infeasible = "ControllerResizeInfeasible: quota exceeded"
					return c
				}()},
				Pods: pods(0),
			},
			want: Decision{Failed: []string{
				data0 + " (pod cluster-data-0): ControllerResizeInfeasible: quota exceeded",
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.role)
			if !equalDecisions(got, tc.want) {
				t.Errorf("Decide() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func equalDecisions(a, b Decision) bool {
	return a.Recreate == b.Recreate &&
		slices.EqualFunc(a.Patches, b.Patches, func(x, y Patch) bool {
			return x.Claim == y.Claim && x.Size.Cmp(y.Size) == 0
		}) &&
		slices.Equal(a.Growing, b.Growing) &&
		slices.Equal(a.Errors, b.Errors) &&
		slices.Equal(a.Failed, b.Failed)
}

func statefulSet(lib, dumps string) *appsv1.StatefulSet {
	template := func(name, size string) corev1.PersistentVolumeClaim {
		return corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			}},
		}
	}
	return &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{template("lib-storage", lib), template("core-dumps", dumps)},
	}}
}

func TestKeepLiveSizes(t *testing.T) {
	desired := statefulSet("10Gi", "2Gi")
	KeepLiveSizes(desired, statefulSet("1Gi", "1Gi"))
	got := TemplateSizes(desired)
	gotLib, gotDumps := got[lib], got[dumps]
	if gotLib.Cmp(size("1Gi")) != 0 || gotDumps.Cmp(size("1Gi")) != 0 {
		t.Errorf("KeepLiveSizes() left %v, want the live 1Gi on both templates", got)
	}

	desired = statefulSet("10Gi", "2Gi")
	KeepLiveSizes(desired, nil)
	if got := TemplateSizes(desired)[lib]; got.Cmp(size("10Gi")) != 0 {
		t.Errorf("KeepLiveSizes() without a live StatefulSet changed the desired sizes to %v", got)
	}
}

func TestObserve(t *testing.T) {
	templates := []string{lib, dumps}
	pvc := func(name string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			}},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:    corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		}
	}

	t.Run("reads the template, ordinal, sizes and pending resize", func(t *testing.T) {
		in := pvc("core-dumps-cluster-data-12")
		in.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
			Type: pending, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(resizedAt),
		}}
		got, ok := Observe(sts, templates, in)
		if !ok {
			t.Fatal("Observe() did not recognise the claim")
		}
		if got.Template != dumps || got.Ordinal != 12 || !got.Bound || got.Request.Cmp(size("10Gi")) != 0 ||
			got.Capacity.Cmp(size("1Gi")) != 0 || !got.ResizePending {
			t.Errorf("Observe() = %+v", got)
		}
	})

	t.Run("reads an infeasible resize with its error", func(t *testing.T) {
		in := pvc(data0)
		in.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
			corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInfeasible,
		}
		in.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
			Type: corev1.PersistentVolumeClaimControllerResizeError, Status: corev1.ConditionTrue, Message: "quota exceeded",
		}}
		got, _ := Observe(sts, templates, in)
		if got.Infeasible != "ControllerResizeInfeasible: quota exceeded" {
			t.Errorf("Observe().Infeasible = %q", got.Infeasible)
		}
	})

	t.Run("reads a resize the driver is still retrying, whitespace collapsed", func(t *testing.T) {
		in := pvc(data0)
		in.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
			corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInProgress,
		}
		in.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
			Type: corev1.PersistentVolumeClaimControllerResizeError, Status: corev1.ConditionTrue,
			Message: "RESPONSE 409:\n  409 Conflict\nERROR CODE: OperationNotAllowed",
		}}
		got, _ := Observe(sts, templates, in)
		if got.Infeasible != "" || got.ResizeError != "RESPONSE 409: 409 Conflict ERROR CODE: OperationNotAllowed" {
			t.Errorf("Observe() = Infeasible %q, ResizeError %q", got.Infeasible, got.ResizeError)
		}
	})

	t.Run("ignores the error of a step the claim is past", func(t *testing.T) {
		in := pvc(data0)
		in.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
			corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
		}
		in.Status.Conditions = []corev1.PersistentVolumeClaimCondition{
			{Type: corev1.PersistentVolumeClaimControllerResizeError, Status: corev1.ConditionTrue, Message: "stale"},
			{Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue},
		}
		got, _ := Observe(sts, templates, in)
		if got.ResizeError != "" || !got.ResizePending {
			t.Errorf("Observe() = ResizeError %q, ResizePending %v", got.ResizeError, got.ResizePending)
		}
	})

	for _, name := range []string{
		"lib-storage-cluster-coordinator-0", // another StatefulSet's
		"lib-storage-cluster-data-x",
		"lib-storage-cluster-data-01",
		"tmp-cluster-data-0", // a template the role does not have
	} {
		t.Run("ignores "+name, func(t *testing.T) {
			if _, ok := Observe(sts, templates, pvc(name)); ok {
				t.Errorf("Observe() recognised %s", name)
			}
		})
	}
}
