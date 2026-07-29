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

// Package planner computes the ordered registration commands that drive an
// observed Memgraph HA cluster toward its declared topology. The logic is a
// pure diff — declared topology plus observed SHOW INSTANCES output in,
// commands out (empty when converged) — so reconciliation stays idempotent
// and read-before-write: only missing registrations are re-issued, and a MAIN is
// promoted only when the cluster has none. After bootstrap, failover belongs to
// the Raft coordinators; the planner never overrides a MAIN that is staying.
//
// Members a lowered replica count is retiring are the one thing the planner
// removes, and it orders each removal so that Memgraph never has to refuse it: a
// retiring MAIN is demoted and a survivor promoted in its place before any
// UNREGISTER INSTANCE, and a retiring coordinator is only ever removed from Raft
// while it is not the leader.
//
// Moving MAIN off a retiring instance is the one thing here that can lose data,
// so it has a precondition the rest do not: a survivor that is both reachable and
// fully caught up with the MAIN, per SHOW REPLICATION LAG. Without one the
// retirement is planned as nothing at all and the retiring MAIN keeps serving —
// waiting is always better than promoting onto an instance that never received
// the writes.
//
// One command breaks the pure-diff mould: YIELD LEADERSHIP, which moves
// coordinator leadership off a retiring coordinator so it can be removed at all.
// It cannot name a successor, so its outcome is the one thing the planner cannot
// predict — which is why it is always the last command of a plan. Everything the
// planner can still order safely goes out ahead of it in the same pass, and the
// caller re-observes the cluster under whichever coordinator won the election.
package planner

import (
	"context"
	"fmt"

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// Topology is the declared cluster registration state: every coordinator and
// data instance the CR says must exist, with the addresses each advertises,
// plus the members on their way out when a count was lowered.
type Topology struct {
	Coordinators  []memgraph.CoordinatorSpec
	DataInstances []memgraph.DataInstanceSpec

	// RetiringCoordinators and RetiringDataInstances are the members a lowered
	// replica count is shedding: still registered and still running, but no
	// longer declared. They are empty while a cluster grows or holds its size.
	//
	// A retiring data instance is demoted if it holds MAIN and then
	// unregistered, so the cluster stops expecting it before its pod goes. A
	// retiring coordinator is removed from the Raft cluster, which Raft only
	// allows for a member that is not the leader — so leadership is yielded away
	// from a retiring leader first.
	RetiringCoordinators  []memgraph.CoordinatorSpec
	RetiringDataInstances []memgraph.DataInstanceSpec
}

// Command is one registration step to execute against the coordinator leader.
type Command interface {
	Run(ctx context.Context, client memgraph.Client) error
	fmt.Stringer
}

// AddCoordinator adds one declared coordinator to the Raft cluster.
type AddCoordinator struct {
	Coordinator memgraph.CoordinatorSpec
}

// Run implements Command.
func (c AddCoordinator) Run(ctx context.Context, client memgraph.Client) error {
	return client.AddCoordinator(ctx, c.Coordinator)
}

func (c AddCoordinator) String() string {
	return fmt.Sprintf("ADD COORDINATOR %d", c.Coordinator.ID)
}

// RegisterInstance registers one declared data instance with the cluster.
type RegisterInstance struct {
	Instance memgraph.DataInstanceSpec
}

// Run implements Command.
func (c RegisterInstance) Run(ctx context.Context, client memgraph.Client) error {
	return client.RegisterInstance(ctx, c.Instance)
}

func (c RegisterInstance) String() string {
	return "REGISTER INSTANCE " + c.Instance.Name
}

// SetInstanceToMain promotes the named data instance to MAIN: at bootstrap,
// when the cluster has no MAIN yet, and after a retiring MAIN was demoted.
type SetInstanceToMain struct {
	Name string
}

// Run implements Command.
func (c SetInstanceToMain) Run(ctx context.Context, client memgraph.Client) error {
	return client.SetInstanceToMain(ctx, c.Name)
}

func (c SetInstanceToMain) String() string {
	return fmt.Sprintf("SET INSTANCE %s TO MAIN", c.Name)
}

// DemoteInstance turns a retiring MAIN back into a replica, which is what makes
// it unregisterable. It is only ever aimed at an instance on its way out of the
// cluster: demoting one that is staying would be the operator overriding a
// failover decision that belongs to the coordinators.
type DemoteInstance struct {
	Name string
}

// Run implements Command.
func (c DemoteInstance) Run(ctx context.Context, client memgraph.Client) error {
	return client.DemoteInstance(ctx, c.Name)
}

func (c DemoteInstance) String() string {
	return "DEMOTE INSTANCE " + c.Name
}

// UnregisterInstance removes a retiring data instance from the cluster, so the
// coordinators stop expecting it before its pod is shed.
type UnregisterInstance struct {
	Name string
}

// Run implements Command.
func (c UnregisterInstance) Run(ctx context.Context, client memgraph.Client) error {
	return client.UnregisterInstance(ctx, c.Name)
}

func (c UnregisterInstance) String() string {
	return "UNREGISTER INSTANCE " + c.Name
}

// RemoveCoordinator drops a retiring coordinator from the Raft cluster, so its
// vote is gone before its pod is. It is never aimed at the observed leader: Raft
// refuses to remove its own leader, which is what YieldLeadership is for.
//
// The removed coordinator keeps running and keeps its state — NuRaft only stops
// it campaigning — so nothing here has to be undone before a raised count adds
// it back on its retained volume.
type RemoveCoordinator struct {
	Coordinator memgraph.CoordinatorSpec
}

// Run implements Command.
func (c RemoveCoordinator) Run(ctx context.Context, client memgraph.Client) error {
	return client.RemoveCoordinator(ctx, c.Coordinator.ID)
}

func (c RemoveCoordinator) String() string {
	return fmt.Sprintf("REMOVE COORDINATOR %d", c.Coordinator.ID)
}

// YieldLeadership hands Raft leadership away from the retiring coordinator that
// currently holds it, which is the only way it can then be removed. It runs on
// the leader — the connection the caller already holds — and cannot name a
// successor, so the plan it ends says nothing about who takes over: the caller
// re-observes the cluster and plans again under the new leader.
type YieldLeadership struct {
	// Leader is the retiring coordinator giving leadership up. It is carried for
	// the sake of whoever is watching the scale-down; the query itself has no
	// argument, and no successor can be named.
	Leader string
}

// Run implements Command.
func (c YieldLeadership) Run(ctx context.Context, client memgraph.Client) error {
	return client.YieldLeadership(ctx)
}

func (c YieldLeadership) String() string {
	return "YIELD LEADERSHIP"
}

// Plan diffs the declared topology against the observed instances and returns
// the commands still needed, in execution order: coordinators before data
// instances (registration requires a formed Raft cluster), then the retirement
// of the members a lowered count sheds — demote a retiring MAIN, promote a
// survivor in its place, unregister every retiring data instance, remove every
// retiring coordinator from Raft. The promotion sits between the demotion and the
// unregistrations so that no UNREGISTER INSTANCE is ever aimed at an observed
// MAIN, and so the cluster is MAIN-less only for the few milliseconds between two
// queries of the same pass.
//
// The handover off a retiring MAIN is conditional on a survivor being able to
// take it: reachable, and holding every transaction the MAIN has committed. When
// none is, the demotion, the promotion and that instance's unregistration are all
// left out of the plan — the retiring MAIN stays MAIN and stays registered, and a
// later pass tries again once a survivor has caught up. The rest of the
// retirement still goes out: retiring instances that are not MAIN are
// unregistered, and retiring coordinators are removed, because neither depends on
// where MAIN sits.
//
// A retiring coordinator that holds Raft leadership cannot be removed at all, so
// the plan ends with YIELD LEADERSHIP instead and stops there — the retiring
// coordinators that are not the leader still go out ahead of it in that same
// pass. Nothing follows a yield, because nothing after it could be planned: the
// election picks the next leader, and the caller has to observe the cluster again
// to learn who won.
//
// Instances the cluster knows but the topology neither declares nor retires are
// left untouched: the retiring set is bounded by the operator's own prior apply,
// so an instance a human registered is never removed.
func Plan(declared Topology, observed []memgraph.Instance, lag []memgraph.ReplicationLag) []Command {
	registered := index(observed)
	retiring := retiringNames(declared)

	// Which instance holds MAIN, and whether it is one on its way out. A retiring
	// MAIN is the cluster's MAIN for as long as it stays: it stops counting as one
	// only once this pass commits to demoting it, which is what the handover below
	// decides.
	retiringMain, hasMain := "", false
	for _, instance := range observed {
		switch {
		case !instance.IsMain():
		case retiring[instance.Name]:
			retiringMain = instance.Name
		default:
			hasMain = true
		}
	}
	// The survivor a retiring MAIN can hand over to, and the empty string when
	// none qualifies — which is what defers the whole retirement to a later pass.
	successor := ""
	if retiringMain != "" {
		successor = handoverTarget(declared, registered, indexLag(lag))
	}
	handover := retiringMain != "" && successor != ""

	var commands []Command
	for _, coordinator := range declared.Coordinators {
		if !coordinatorRegistered(registered, coordinator) {
			commands = append(commands, AddCoordinator{Coordinator: coordinator})
		}
	}
	for _, instance := range declared.DataInstances {
		if _, ok := registered[instance.Name]; !ok {
			commands = append(commands, RegisterInstance{Instance: instance})
		}
	}
	if handover {
		commands = append(commands, DemoteInstance{Name: retiringMain})
	}
	switch {
	case handover && !hasMain:
		// The demotion above left the cluster MAIN-less on purpose; the survivor
		// picked for the handover takes over in the next command.
		commands = append(commands, SetInstanceToMain{Name: successor})
	case retiringMain == "" && !hasMain && len(declared.DataInstances) > 0:
		// No MAIN and none retiring: a fresh bootstrap, a MAIN whose promotion never
		// landed, or a pass that died between a retiring MAIN's demotion and the
		// promotion meant to follow it. Lag is measured against a MAIN, so with none
		// there is nothing to measure and this promotion goes by reachability alone.
		// That is also why the handover is gated before the demotion rather than
		// after: it is the last moment at which the choice is still free.
		commands = append(commands, SetInstanceToMain{Name: promotionTarget(declared, registered)})
	}
	for _, instance := range declared.RetiringDataInstances {
		if _, ok := registered[instance.Name]; !ok {
			continue
		}
		if instance.Name == retiringMain && !handover {
			// Memgraph refuses to unregister the MAIN, and the demotion that would
			// make this one unregisterable is waiting for a survivor to take over.
			continue
		}
		commands = append(commands, UnregisterInstance{Name: instance.Name})
	}

	leader := leaderName(observed)
	yieldFrom := ""
	for _, coordinator := range declared.RetiringCoordinators {
		if coordinator.Name() == leader {
			// Raft refuses to remove its own leader, so this one waits for the
			// yield below to move leadership to another member.
			yieldFrom = leader
			continue
		}
		if coordinatorRegistered(registered, coordinator) {
			commands = append(commands, RemoveCoordinator{Coordinator: coordinator})
		}
	}
	if yieldFrom != "" {
		commands = append(commands, YieldLeadership{Leader: yieldFrom})
	}
	return commands
}

// leaderName is the coordinator the observed view reports as Raft leader, or the
// empty string when it reports none.
func leaderName(observed []memgraph.Instance) string {
	for _, instance := range observed {
		if instance.IsLeader() {
			return instance.Name
		}
	}
	return ""
}

// Retired reports whether every member a lowered replica count is shedding has
// left the cluster: no retiring data instance is still registered, and no
// retiring coordinator is still a Raft member. It is the precondition for
// shedding their pods.
//
// Plan coming back empty does not establish that on its own. A retirement whose
// handover is waiting for a caught-up survivor also plans nothing — there is no
// command that would make a lagging replica ready — so a caller that read
// emptiness as "done" would delete the pod of a registered MAIN. This is the
// question it has to ask instead.
func Retired(declared Topology, observed []memgraph.Instance) bool {
	registered := index(observed)
	for _, instance := range declared.RetiringDataInstances {
		if _, ok := registered[instance.Name]; ok {
			return false
		}
	}
	for _, coordinator := range declared.RetiringCoordinators {
		if coordinatorRegistered(registered, coordinator) {
			return false
		}
	}
	return true
}

// Registered reports how many of the declared coordinators and data instances
// the observed cluster has registered. It is pure observation for the CR's
// status, and it shares Plan's definition of "registered" — so a role's count
// reaches its declared count exactly when Plan stops issuing registrations for
// it.
func Registered(declared Topology, observed []memgraph.Instance) (coordinators, dataInstances int32) {
	registered := index(observed)
	for _, coordinator := range declared.Coordinators {
		if coordinatorRegistered(registered, coordinator) {
			coordinators++
		}
	}
	for _, instance := range declared.DataInstances {
		if _, ok := registered[instance.Name]; ok {
			dataInstances++
		}
	}
	return coordinators, dataInstances
}

// retiringNames is the set of data instances the topology is shedding, keyed by
// the name they are registered under.
func retiringNames(declared Topology) map[string]bool {
	retiring := make(map[string]bool, len(declared.RetiringDataInstances))
	for _, instance := range declared.RetiringDataInstances {
		retiring[instance.Name] = true
	}
	return retiring
}

// index keys the observed cluster view by instance name.
func index(observed []memgraph.Instance) map[string]memgraph.Instance {
	registered := make(map[string]memgraph.Instance, len(observed))
	for _, instance := range observed {
		registered[instance.Name] = instance
	}
	return registered
}

// indexLag keys the observed replication lag by instance name. A name the view
// does not cover reads back as the zero ReplicationLag, which reports itself as
// not caught up — the safe answer for an instance nothing is known about.
func indexLag(lag []memgraph.ReplicationLag) map[string]memgraph.ReplicationLag {
	byInstance := make(map[string]memgraph.ReplicationLag, len(lag))
	for _, instance := range lag {
		byInstance[instance.Instance] = instance
	}
	return byInstance
}

// coordinatorRegistered reports whether the declared coordinator is a member of
// the Raft cluster. A coordinator reports itself in SHOW INSTANCES with an
// empty bolt_server until ADD COORDINATOR is issued for its ID, so presence
// alone does not prove registration.
func coordinatorRegistered(registered map[string]memgraph.Instance, coordinator memgraph.CoordinatorSpec) bool {
	observed, ok := registered[coordinator.Name()]
	return ok && observed.BoltServer != ""
}

// handoverTarget picks the survivor a retiring MAIN hands MAIN over to: the
// lowest-ordinal declared instance the coordinator leader observes as up and that
// SHOW REPLICATION LAG reports as holding every transaction the MAIN has
// committed, in every database. It returns the empty string when no survivor
// qualifies.
//
// Both conditions are needed and neither implies the other. Health says the
// leader can reach the instance, which a caught-up replica can still fail —
// registration outlives reachability, and the lag view is relayed from the MAIN's
// own cached progress for each replica, so an instance that went down a moment
// ago still appears there at the offset it last reached. Lag says the instance
// holds the writes, which a reachable one need not.
//
// There is deliberately no fallback, which is what separates this from
// promotionTarget. A cluster with no MAIN is worse off than one whose promotion
// has to be retried, so that one guesses rather than stall. A retiring MAIN is
// still serving: waiting costs nothing but the scale-down's completion, while
// promoting a lagging survivor discards every transaction it never received.
func handoverTarget(
	declared Topology,
	registered map[string]memgraph.Instance,
	lag map[string]memgraph.ReplicationLag,
) string {
	for _, instance := range declared.DataInstances {
		if observed, ok := registered[instance.Name]; !ok || !observed.IsUp() {
			continue
		}
		if lag[instance.Name].IsCaughtUp() {
			return instance.Name
		}
	}
	return ""
}

// promotionTarget picks the data instance to promote when the cluster has no
// MAIN: the lowest-ordinal declared instance the cluster observes as up.
// Promoting a down instance would only write the intent to Raft and leave the
// cluster MAIN-less until the coordinators retried it, so an instance that is
// known to be reachable is preferred over a lower-ordinal one that is not.
//
// Only declared instances are candidates, which is what makes the rule serve a
// retirement too: promoting a survivor is the same choice as promoting at
// bootstrap, and a member on its way out can never be the target.
//
// The first declared instance is the fallback, which is what a fresh bootstrap
// uses: nothing is observed yet at the point its registrations are planned.
func promotionTarget(declared Topology, registered map[string]memgraph.Instance) string {
	for _, instance := range declared.DataInstances {
		if observed, ok := registered[instance.Name]; ok && observed.IsUp() {
			return instance.Name
		}
	}
	return declared.DataInstances[0].Name
}
