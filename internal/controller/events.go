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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// Event reasons, one per action the operator takes on a cluster. Every Event
// is recorded on the MemgraphDBCluster, with the pod, claim or StatefulSet the
// action touched as the related object where there is one. They record what
// the operator did, never what it is waiting for: waiting is what the
// conditions say, and an Event repeated every pass would bury the actions.
//
// The related object is what keeps two actions two Events. client-go's
// recorder folds an Event into an earlier one with the same type, reason,
// action, regarding and related object, seen within six minutes, into a
// count on the earlier one's note; the note itself is not compared. With no
// related object, adding coordinator_1 and then coordinator_2 reads as adding
// coordinator_1 twice. So every action names the member it touched, and a
// step that SETs several settings is one Event listing them: per pod for the
// database settings, per pass, on the Raft leader's pod, for the coordinator
// settings.
//
// The Bolt commands are the reason Events exist at all: the Kubernetes audit
// log sees every write the operator makes to the API server, but nothing it
// sends to Memgraph. Each has a reason for the command Memgraph accepted and
// one for the command it refused, a Warning, so the reason alone says which
// command failed.
const (
	EventReasonCoordinatorAdded              = "CoordinatorAdded"
	EventReasonCoordinatorAdditionRefused    = "CoordinatorAdditionRefused"
	EventReasonInstanceRegistered            = "InstanceRegistered"
	EventReasonInstanceRegistrationRefused   = "InstanceRegistrationRefused"
	EventReasonMainPromoted                  = "MainPromoted"
	EventReasonMainPromotionRefused          = "MainPromotionRefused"
	EventReasonMainDemoted                   = "MainDemoted"
	EventReasonMainDemotionRefused           = "MainDemotionRefused"
	EventReasonInstanceUnregistered          = "InstanceUnregistered"
	EventReasonInstanceUnregistrationRefused = "InstanceUnregistrationRefused"
	EventReasonCoordinatorRemoved            = "CoordinatorRemoved"
	EventReasonCoordinatorRemovalRefused     = "CoordinatorRemovalRefused"
	EventReasonLeadershipYielded             = "LeadershipYielded"
	EventReasonLeadershipYieldRefused        = "LeadershipYieldRefused"
	EventReasonBoltServerUpdated             = "BoltServerUpdated"
	EventReasonBoltServerUpdateRefused       = "BoltServerUpdateRefused"
	EventReasonSettingApplied                = "SettingApplied"
	EventReasonSettingRejected               = "SettingRejected"
	EventReasonCoordinatorSettingApplied     = "CoordinatorSettingApplied"
	EventReasonPodRestarted                  = "PodRestarted"
	EventReasonStatefulSetShrunk             = "StatefulSetShrunk"
	EventReasonVolumeExpansionRequested      = "VolumeExpansionRequested"
	EventReasonStatefulSetOrphaned           = "StatefulSetOrphaned"
	EventReasonSystemUserCreated             = "SystemUserCreated"
	EventReasonSystemUserHashCreated         = "SystemUserHashCreated"
	EventReasonSystemUserHashUpdated         = "SystemUserHashUpdated"
)

// Event actions, the events.k8s.io field saying what the operator did to the
// regarding object's members; a reason says why it is worth knowing.
const (
	eventActionRegister              = "Register"
	eventActionPromote               = "Promote"
	eventActionDemote                = "Demote"
	eventActionUnregister            = "Unregister"
	eventActionYield                 = "YieldLeadership"
	eventActionUpdateConfig          = "UpdateConfig"
	eventActionSetCoordinatorSetting = "SetCoordinatorSetting"
	// The three steps that SET database settings on a pod each have their
	// own action, because two of them can SET on the same pod in one pass
	// and would otherwise be one Event.
	eventActionSetLicense        = "SetLicense"
	eventActionSetAWSCredentials = "SetAWSCredentials"
	eventActionSetRunTimeFlags   = "SetRunTimeFlags"
	eventActionRestart           = "Restart"
	eventActionScaleDown         = "ScaleDown"
	eventActionExpandVolume      = "ExpandVolume"
	eventActionRecreate          = "RecreateStatefulSet"
)

// event records one Normal Event on the cluster.
func (r *MemgraphDBClusterReconciler) event(
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
	related runtime.Object,
	reason, action, note string,
	args ...any,
) {
	r.Recorder.Eventf(cluster, related, corev1.EventTypeNormal, reason, action, truncateMessage(fmt.Sprintf(note, args...)))
}

// warning records one Warning Event on the cluster.
func (r *MemgraphDBClusterReconciler) warning(
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
	related runtime.Object,
	reason, action, note string,
	args ...any,
) {
	r.Recorder.Eventf(cluster, related, corev1.EventTypeWarning, reason, action, truncateMessage(fmt.Sprintf(note, args...)))
}

// podRef is a pod of the cluster as an Event's related object. The recorder
// needs only the kind, name and namespace to reference it.
func podRef(cluster *memgraphcomv1alpha1.MemgraphDBCluster, name string) runtime.Object {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
}

// statefulSetRef is a StatefulSet of the cluster as an Event's related object.
func statefulSetRef(cluster *memgraphcomv1alpha1.MemgraphDBCluster, name string) runtime.Object {
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
}

// dataInstancePodRef is the pod running the named data instance. A name that
// does not parse yields no related object rather than a wrong one.
func dataInstancePodRef(cluster *memgraphcomv1alpha1.MemgraphDBCluster, instance string) runtime.Object {
	ordinal, err := resources.DataInstanceOrdinal(instance)
	if err != nil {
		return nil
	}
	return podRef(cluster, resources.PodName(resources.DataName(cluster), ordinal))
}

// coordinatorPodRef is the pod running the named coordinator, with the same
// fallback.
func coordinatorPodRef(cluster *memgraphcomv1alpha1.MemgraphDBCluster, coordinator string) runtime.Object {
	ordinal, err := resources.CoordinatorOrdinal(coordinator)
	if err != nil {
		return nil
	}
	return podRef(cluster, resources.PodName(resources.CoordinatorName(cluster), ordinal))
}

// recordingClient records an Event for every registration command the leader
// answers, accepted or refused, so the record is of what Memgraph was asked
// and said rather than of what the plan intended: a promotion tries its
// candidates in order inside one command, and only the client sees which of
// them the coordinators took.
type recordingClient struct {
	memgraph.Client
	r       *MemgraphDBClusterReconciler
	cluster *memgraphcomv1alpha1.MemgraphDBCluster

	// leader is the retiring coordinator a YieldLeadership in this plan moves
	// leadership away from; the query names no one, so the plan says who.
	leader string
}

func (c recordingClient) AddCoordinator(ctx context.Context, coordinator memgraph.CoordinatorSpec) error {
	err := c.Client.AddCoordinator(ctx, coordinator)
	c.record(err, coordinatorPodRef(c.cluster, coordinator.Name()),
		EventReasonCoordinatorAdded, EventReasonCoordinatorAdditionRefused, eventActionRegister,
		"Added coordinator %s to the Raft cluster", coordinator.Name())
	return err
}

func (c recordingClient) RegisterInstance(ctx context.Context, instance memgraph.DataInstanceSpec) error {
	err := c.Client.RegisterInstance(ctx, instance)
	c.record(err, dataInstancePodRef(c.cluster, instance.Name),
		EventReasonInstanceRegistered, EventReasonInstanceRegistrationRefused, eventActionRegister,
		"Registered data instance %s with the coordinators", instance.Name)
	return err
}

// SetInstanceToMain records each refused candidate, and is the one command
// whose refusal is not the end of it: the command falls back to its next
// candidate, and the instance that did become MAIN gets the MainPromoted.
func (c recordingClient) SetInstanceToMain(ctx context.Context, name string) error {
	err := c.Client.SetInstanceToMain(ctx, name)
	if err != nil {
		c.r.warning(c.cluster, dataInstancePodRef(c.cluster, name), EventReasonMainPromotionRefused, eventActionPromote,
			"The coordinators refused to promote data instance %s to MAIN: %v", name, err)
		return err
	}
	c.r.event(c.cluster, dataInstancePodRef(c.cluster, name), EventReasonMainPromoted, eventActionPromote,
		"Promoted data instance %s to MAIN", name)
	return nil
}

func (c recordingClient) DemoteInstance(ctx context.Context, name string) error {
	err := c.Client.DemoteInstance(ctx, name)
	c.record(err, dataInstancePodRef(c.cluster, name),
		EventReasonMainDemoted, EventReasonMainDemotionRefused, eventActionDemote,
		"Demoted retiring data instance %s from MAIN to REPLICA", name)
	return err
}

func (c recordingClient) UnregisterInstance(ctx context.Context, name string) error {
	err := c.Client.UnregisterInstance(ctx, name)
	c.record(err, dataInstancePodRef(c.cluster, name),
		EventReasonInstanceUnregistered, EventReasonInstanceUnregistrationRefused, eventActionUnregister,
		"Unregistered retiring data instance %s from the coordinators", name)
	return err
}

func (c recordingClient) RemoveCoordinator(ctx context.Context, id int32) error {
	err := c.Client.RemoveCoordinator(ctx, id)
	name := memgraph.CoordinatorSpec{ID: id}.Name()
	c.record(err, coordinatorPodRef(c.cluster, name),
		EventReasonCoordinatorRemoved, EventReasonCoordinatorRemovalRefused, eventActionUnregister,
		"Removed retiring coordinator %s from the Raft cluster", name)
	return err
}

func (c recordingClient) YieldLeadership(ctx context.Context) error {
	err := c.Client.YieldLeadership(ctx)
	c.record(err, coordinatorPodRef(c.cluster, c.leader),
		EventReasonLeadershipYielded, EventReasonLeadershipYieldRefused, eventActionYield,
		"Yielded Raft leadership of retiring coordinator %s to another coordinator", c.leader)
	return err
}

func (c recordingClient) UpdateCoordinatorBoltServer(ctx context.Context, id int32, boltServer string) error {
	err := c.Client.UpdateCoordinatorBoltServer(ctx, id, boltServer)
	name := memgraph.CoordinatorSpec{ID: id}.Name()
	c.record(err, coordinatorPodRef(c.cluster, name),
		EventReasonBoltServerUpdated, EventReasonBoltServerUpdateRefused, eventActionUpdateConfig,
		"Updated the bolt_server coordinator %s announces to %s", name, boltServer)
	return err
}

func (c recordingClient) UpdateInstanceBoltServer(ctx context.Context, name, boltServer string) error {
	err := c.Client.UpdateInstanceBoltServer(ctx, name, boltServer)
	c.record(err, dataInstancePodRef(c.cluster, name),
		EventReasonBoltServerUpdated, EventReasonBoltServerUpdateRefused, eventActionUpdateConfig,
		"Updated the bolt_server data instance %s announces to %s", name, boltServer)
	return err
}

// record is the Normal Event under accepted that an accepted command earns,
// or the Warning under refused that a refused one does; the refusal carries
// what the action would have been and Memgraph's error. related is the pod of
// the member the command was about.
func (c recordingClient) record(
	err error, related runtime.Object, accepted, refused, action, note string, args ...any,
) {
	if err != nil {
		c.r.warning(c.cluster, related, refused, action, "%s refused: %v", fmt.Sprintf(note, args...), err)
		return
	}
	c.r.event(c.cluster, related, accepted, action, note, args...)
}
