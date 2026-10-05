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

// Package volumes decides how a grown claim size reaches a role's
// PersistentVolumeClaims. Kubernetes forbids changing a StatefulSet's
// volumeClaimTemplates, so a larger size takes three steps, each one read off
// the cluster rather than remembered: every claim of the role is patched to
// the new size, and the StatefulSet is deleted with its pods and claims
// orphaned and recreated around them with the new template. The storage
// provider and the kubelet grow each volume and its filesystem from there.
//
// The recreate restarts nothing. The StatefulSet controller adopts an orphaned
// pod when its volumes name the claims the templates would give its ordinal —
// names, never sizes — and the revision a pod is measured against hashes the
// pod template alone, which a size change leaves as it was. That is what tells
// a size change apart from adding or removing a claim template, which changes
// the pod's volumes, fails adoption, and wedges the restart order; those stay
// refused at admission.
//
// Every claim carrying the role's labels is patched, a claim retained from an
// earlier scale-down included: a re-grow reattaches it as it is, and the
// recreated template's size only ever reaches claims the StatefulSet
// controller creates.
//
// No pod is ever restarted for a resize. A current CSI driver grows the
// filesystem under the running pod, the kubelet doing it on its next sync;
// one that cannot refuses to grow a volume any pod uses at all, which a
// restart does not change — the StatefulSet recreates the pod at once — so
// such a resize needs the pod gone for its whole length, an outage that is
// the user's to choose. The claim is reported growing, with its pod, until
// it is done.
//
// The decision is a pure function of the role's desired and live claim
// templates, its claims and its pods. It issues nothing; the caller patches
// and deletes.
package volumes

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Role is one StatefulSet's claims as the decision sees them.
type Role struct {
	// StatefulSet is the role's StatefulSet name, which every claim name
	// embeds and every message names.
	StatefulSet string

	// Desired is the size of each claim template the spec describes, by
	// template name.
	Desired map[string]resource.Quantity

	// Live is the size of each claim template the existing StatefulSet
	// carries, by template name, or nil when there is none: a StatefulSet
	// that does not exist is created from Desired and needs no recreate.
	Live map[string]resource.Quantity

	// Claims are the role's claims, the ones retained beyond its replica
	// count included.
	Claims []Claim

	// Pods are the role's pods that exist, by ordinal: a claim whose ordinal
	// has a pod is mounted.
	Pods []Pod
}

// Claim is one PersistentVolumeClaim of a role.
type Claim struct {
	Name     string
	Template string
	Ordinal  int32

	// Bound reports the claim bound to a volume. Kubernetes accepts a new size
	// only on a bound claim, and an unbound one is waiting on its pod's
	// scheduling, not on a resize.
	Bound bool

	// Request is the claim's spec.resources.requests.storage, and Capacity its
	// status.capacity.storage: what was asked for and what the filesystem
	// holds.
	Request  resource.Quantity
	Capacity resource.Quantity

	// ResizePending reports that the volume grew and only its filesystem is
	// left to grow, which the kubelet does on a node that mounts it: the
	// claim's FileSystemResizePending condition.
	ResizePending bool

	// Infeasible is the storage provider's refusal of a resize it had
	// accepted, from the claim's allocatedResourceStatuses, or empty.
	Infeasible string

	// ResizeError is the last error the resizer or the kubelet reported
	// growing the claim — its ControllerResizeError or NodeResizeError
	// condition, whitespace collapsed — while it is still retrying, or empty.
	// A driver that cannot grow a volume in use says so here.
	ResizeError string
}

// Pod is one existing pod of a role.
type Pod struct {
	Name    string
	Ordinal int32
}

// Patch is one claim to grow.
type Patch struct {
	Claim string
	Size  resource.Quantity
}

// Decision is what the caller does with one role's claims this pass.
type Decision struct {
	// Patches are the claims to grow, in name order.
	Patches []Patch

	// Recreate says to delete the StatefulSet with its dependents orphaned, so
	// the next apply recreates it with the desired claim templates. Only set
	// once no claim is left to patch: claims first, because a resize the API
	// server refuses must leave the StatefulSet as it is.
	Recreate bool

	// Growing describes the claims whose new size is not usable yet, in name
	// order, each with the pod mounting it if one does.
	Growing []string

	// Errors are the errors the storage driver reports for claims in
	// Growing, as "<claim>: <error>", in name order.
	Errors []string

	// Failed describes each claim whose resize the storage provider gave up
	// on.
	Failed []string
}

// Decide is the one role's step toward every claim holding the desired size.
//
// A claim is done once its filesystem holds what it asks for. A claim nothing
// mounts is done once its volume has grown: its filesystem grows only when a
// pod next mounts it, so a retained claim waiting for that would keep the
// cluster from ever converging after a resize.
func Decide(role Role) Decision {
	var decision Decision
	pods := make(map[int32]Pod, len(role.Pods))
	for _, pod := range role.Pods {
		pods[pod.Ordinal] = pod
	}

	claims := slices.Clone(role.Claims)
	slices.SortFunc(claims, func(a, b Claim) int { return strings.Compare(a.Name, b.Name) })
	for _, claim := range claims {
		want, ok := role.Desired[claim.Template]
		if !ok || !claim.Bound {
			continue
		}
		pod, mounted := pods[claim.Ordinal]
		named := claim.Name
		if mounted {
			named = fmt.Sprintf("%s (pod %s)", claim.Name, pod.Name)
		}
		switch {
		case claim.Request.Cmp(want) < 0:
			decision.Patches = append(decision.Patches, Patch{Claim: claim.Name, Size: want})
			decision.Growing = append(decision.Growing, named)
		case claim.Infeasible != "":
			decision.Failed = append(decision.Failed, fmt.Sprintf("%s: %s", named, claim.Infeasible))
		case claim.Capacity.Cmp(claim.Request) >= 0:
		case mounted || !claim.ResizePending:
			decision.Growing = append(decision.Growing, named)
			if claim.ResizeError != "" {
				decision.Errors = append(decision.Errors, claim.Name+": "+claim.ResizeError)
			}
		}
	}

	decision.Recreate = role.Live != nil && len(decision.Patches) == 0 && templatesGrew(role.Desired, role.Live)
	return decision
}

// templatesGrew reports whether any claim template is to be larger than the
// live one, compared as quantities so a unit change is not a change. A live
// template larger than the spec was not made from it — admission never lets
// a size shrink — and recreating it smaller would grow nothing.
func templatesGrew(desired, live map[string]resource.Quantity) bool {
	for name, size := range desired {
		if current, ok := live[name]; ok && current.Cmp(size) < 0 {
			return true
		}
	}
	return false
}

// TemplateSizes is each claim template's requested size, by template name.
func TemplateSizes(sts *appsv1.StatefulSet) map[string]resource.Quantity {
	sizes := make(map[string]resource.Quantity, len(sts.Spec.VolumeClaimTemplates))
	for _, claim := range sts.Spec.VolumeClaimTemplates {
		sizes[claim.Name] = claim.Spec.Resources.Requests[corev1.ResourceStorage]
	}
	return sizes
}

// KeepLiveSizes sets each claim template of a desired StatefulSet to the size
// the live one carries. Kubernetes refuses any change to a live StatefulSet's
// claim templates, so an apply always restates the sizes it already has; the
// desired ones arrive only through a recreate.
func KeepLiveSizes(desired, live *appsv1.StatefulSet) {
	if live == nil {
		return
	}
	sizes := TemplateSizes(live)
	for i := range desired.Spec.VolumeClaimTemplates {
		claim := &desired.Spec.VolumeClaimTemplates[i]
		if size, ok := sizes[claim.Name]; ok {
			claim.Spec.Resources.Requests[corev1.ResourceStorage] = size
		}
	}
}

// Observe reads one PersistentVolumeClaim as a claim of the named StatefulSet,
// reporting false for a claim no template of it names: the StatefulSet
// controller names a claim <template>-<statefulset>-<ordinal>.
func Observe(statefulSet string, templates []string, pvc *corev1.PersistentVolumeClaim) (Claim, bool) {
	for _, template := range templates {
		suffix, ok := strings.CutPrefix(pvc.Name, template+"-"+statefulSet+"-")
		if !ok {
			continue
		}
		ordinal, err := strconv.ParseInt(suffix, 10, 32)
		if err != nil || ordinal < 0 || strconv.FormatInt(ordinal, 10) != suffix {
			continue
		}
		claim := Claim{
			Name:     pvc.Name,
			Template: template,
			Ordinal:  int32(ordinal),
			Bound:    pvc.Status.Phase == corev1.ClaimBound,
			Request:  pvc.Spec.Resources.Requests[corev1.ResourceStorage],
			Capacity: pvc.Status.Capacity[corev1.ResourceStorage],
		}
		for _, condition := range pvc.Status.Conditions {
			if condition.Type == corev1.PersistentVolumeClaimFileSystemResizePending &&
				condition.Status == corev1.ConditionTrue {
				claim.ResizePending = true
			}
		}
		if pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage] ==
			corev1.PersistentVolumeClaimNodeResizePending {
			claim.ResizePending = true
		}
		claim.Infeasible = infeasible(pvc)
		if claim.Infeasible == "" {
			claim.ResizeError = resizeError(pvc)
		}
		return claim, true
	}
	return Claim{}, false
}

// infeasible is the provider's refusal of an accepted resize, with the
// condition message that explains it when there is one.
func infeasible(pvc *corev1.PersistentVolumeClaim) string {
	status, ok := pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage]
	if !ok {
		return ""
	}
	var errorCondition corev1.PersistentVolumeClaimConditionType
	switch status {
	case corev1.PersistentVolumeClaimControllerResizeInfeasible:
		errorCondition = corev1.PersistentVolumeClaimControllerResizeError
	case corev1.PersistentVolumeClaimNodeResizeInfeasible:
		errorCondition = corev1.PersistentVolumeClaimNodeResizeError
	default:
		return ""
	}
	for _, condition := range pvc.Status.Conditions {
		if condition.Type == errorCondition && condition.Message != "" {
			return fmt.Sprintf("%s: %s", status, collapse(condition.Message))
		}
	}
	return string(status)
}

// resizeError is the message of the resize error condition for the step the
// claim is on, or empty. Only that step's error counts: Kubernetes leaves a
// ControllerResizeError on the claim after the volume has grown, and quoting
// it while the filesystem catches up would blame a step that is done — seen on
// AKS once the pod moved to a VM size that can change an attached disk.
func resizeError(pvc *corev1.PersistentVolumeClaim) string {
	var step corev1.PersistentVolumeClaimConditionType
	switch pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage] {
	case corev1.PersistentVolumeClaimControllerResizeInProgress:
		step = corev1.PersistentVolumeClaimControllerResizeError
	case corev1.PersistentVolumeClaimNodeResizePending, corev1.PersistentVolumeClaimNodeResizeInProgress:
		step = corev1.PersistentVolumeClaimNodeResizeError
	default:
		return ""
	}
	for _, condition := range pvc.Status.Conditions {
		if condition.Type == step && condition.Status == corev1.ConditionTrue && condition.Message != "" {
			return collapse(condition.Message)
		}
	}
	return ""
}

// collapse folds a message's whitespace into single spaces: a cloud API's
// error arrives with its HTTP response laid out over many lines.
func collapse(message string) string {
	return strings.Join(strings.Fields(message), " ")
}
