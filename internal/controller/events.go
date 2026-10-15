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

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// Event reasons, one per action the operator takes on a cluster. Every Event
// is recorded on the MemgraphCluster, with the pod or claim the action touched
// as the related object where there is one. They record what the operator
// did, never what it is waiting for: waiting is what the conditions say, and
// an Event repeated every pass would bury the actions.
//
// The Bolt commands are the reason Events exist at all: the Kubernetes audit
// log sees every write the operator makes to the API server, but nothing it
// sends to Memgraph.
const (
	EventReasonCoordinatorAdded          = "CoordinatorAdded"
	EventReasonInstanceRegistered        = "InstanceRegistered"
	EventReasonMainPromoted              = "MainPromoted"
	EventReasonMainPromotionRefused      = "MainPromotionRefused"
	EventReasonMainDemoted               = "MainDemoted"
	EventReasonInstanceUnregistered      = "InstanceUnregistered"
	EventReasonCoordinatorRemoved        = "CoordinatorRemoved"
	EventReasonLeadershipYielded         = "LeadershipYielded"
	EventReasonBoltServerUpdated         = "BoltServerUpdated"
	EventReasonRegistrationFailed        = "RegistrationFailed"
	EventReasonSettingApplied            = "SettingApplied"
	EventReasonSettingRejected           = "SettingRejected"
	EventReasonCoordinatorSettingApplied = "CoordinatorSettingApplied"
	EventReasonPodRestarted              = "PodRestarted"
	EventReasonStatefulSetShrunk         = "StatefulSetShrunk"
	EventReasonVolumeExpansionRequested  = "VolumeExpansionRequested"
	EventReasonStatefulSetOrphaned       = "StatefulSetOrphaned"
)

// Event actions, the events.k8s.io field saying what the operator did to the
// regarding object's members; a reason says why it is worth knowing.
const (
	eventActionRegister     = "Register"
	eventActionPromote      = "Promote"
	eventActionDemote       = "Demote"
	eventActionUnregister   = "Unregister"
	eventActionYield        = "YieldLeadership"
	eventActionUpdateConfig = "UpdateConfig"
	eventActionSetSetting   = "SetSetting"
	eventActionRestart      = "Restart"
	eventActionScaleDown    = "ScaleDown"
	eventActionExpandVolume = "ExpandVolume"
	eventActionRecreate     = "RecreateStatefulSet"
)

// event records one Normal Event on the cluster.
func (r *MemgraphClusterReconciler) event(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	related runtime.Object,
	reason, action, note string,
	args ...any,
) {
	r.Recorder.Eventf(cluster, related, corev1.EventTypeNormal, reason, action, truncateMessage(fmt.Sprintf(note, args...)))
}

// warning records one Warning Event on the cluster.
func (r *MemgraphClusterReconciler) warning(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	related runtime.Object,
	reason, action, note string,
	args ...any,
) {
	r.Recorder.Eventf(cluster, related, corev1.EventTypeWarning, reason, action, truncateMessage(fmt.Sprintf(note, args...)))
}

// podRef is a pod of the cluster as an Event's related object. The recorder
// needs only the kind, name and namespace to reference it.
func podRef(cluster *memgraphcomv1alpha1.MemgraphCluster, name string) runtime.Object {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
}

// recordingClient records an Event for every registration command the leader
// answers, accepted or refused, so the record is of what Memgraph was asked
// and said rather than of what the plan intended: a promotion tries its
// candidates in order inside one command, and only the client sees which of
// them the coordinators took.
type recordingClient struct {
	memgraph.Client
	r       *MemgraphClusterReconciler
	cluster *memgraphcomv1alpha1.MemgraphCluster

	// leader is the retiring coordinator a YieldLeadership in this plan moves
	// leadership away from; the query names no one, so the plan says who.
	leader string
}

func (c recordingClient) AddCoordinator(ctx context.Context, coordinator memgraph.CoordinatorSpec) error {
	err := c.Client.AddCoordinator(ctx, coordinator)
	c.record(err, EventReasonCoordinatorAdded, eventActionRegister,
		"Added coordinator %s to the Raft cluster", coordinator.Name())
	return err
}

func (c recordingClient) RegisterInstance(ctx context.Context, instance memgraph.DataInstanceSpec) error {
	err := c.Client.RegisterInstance(ctx, instance)
	c.record(err, EventReasonInstanceRegistered, eventActionRegister,
		"Registered data instance %s with the coordinators", instance.Name)
	return err
}

// SetInstanceToMain records a refusal as a Warning of its own rather than a
// failed registration: the command falls back to its next candidate, so a
// refusal is not the end of the promotion, and the instance that did become
// MAIN gets the MainPromoted.
func (c recordingClient) SetInstanceToMain(ctx context.Context, name string) error {
	err := c.Client.SetInstanceToMain(ctx, name)
	if err != nil {
		c.r.warning(c.cluster, nil, EventReasonMainPromotionRefused, eventActionPromote,
			"The coordinators refused to promote data instance %s to MAIN: %v", name, err)
		return err
	}
	c.r.event(c.cluster, nil, EventReasonMainPromoted, eventActionPromote, "Promoted data instance %s to MAIN", name)
	return nil
}

func (c recordingClient) DemoteInstance(ctx context.Context, name string) error {
	err := c.Client.DemoteInstance(ctx, name)
	c.record(err, EventReasonMainDemoted, eventActionDemote,
		"Demoted retiring data instance %s from MAIN to REPLICA", name)
	return err
}

func (c recordingClient) UnregisterInstance(ctx context.Context, name string) error {
	err := c.Client.UnregisterInstance(ctx, name)
	c.record(err, EventReasonInstanceUnregistered, eventActionUnregister,
		"Unregistered retiring data instance %s from the coordinators", name)
	return err
}

func (c recordingClient) RemoveCoordinator(ctx context.Context, id int32) error {
	err := c.Client.RemoveCoordinator(ctx, id)
	c.record(err, EventReasonCoordinatorRemoved, eventActionUnregister,
		"Removed retiring coordinator %s from the Raft cluster", memgraph.CoordinatorSpec{ID: id}.Name())
	return err
}

func (c recordingClient) YieldLeadership(ctx context.Context) error {
	err := c.Client.YieldLeadership(ctx)
	c.record(err, EventReasonLeadershipYielded, eventActionYield,
		"Yielded Raft leadership of retiring coordinator %s to another coordinator", c.leader)
	return err
}

func (c recordingClient) UpdateCoordinatorBoltServer(ctx context.Context, id int32, boltServer string) error {
	err := c.Client.UpdateCoordinatorBoltServer(ctx, id, boltServer)
	c.record(err, EventReasonBoltServerUpdated, eventActionUpdateConfig,
		"Updated the bolt_server coordinator %s announces to %s", memgraph.CoordinatorSpec{ID: id}.Name(), boltServer)
	return err
}

func (c recordingClient) UpdateInstanceBoltServer(ctx context.Context, name, boltServer string) error {
	err := c.Client.UpdateInstanceBoltServer(ctx, name, boltServer)
	c.record(err, EventReasonBoltServerUpdated, eventActionUpdateConfig,
		"Updated the bolt_server data instance %s announces to %s", name, boltServer)
	return err
}

// record is the Normal Event an accepted command earns, or the Warning a
// refused one does; the refusal carries what the action would have been and
// Memgraph's error.
func (c recordingClient) record(err error, reason, action, note string, args ...any) {
	if err != nil {
		c.r.warning(c.cluster, nil, EventReasonRegistrationFailed, action,
			"%s refused: %v", fmt.Sprintf(note, args...), err)
		return
	}
	c.r.event(c.cluster, nil, reason, action, note, args...)
}
