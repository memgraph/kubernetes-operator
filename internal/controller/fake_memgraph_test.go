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
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// fakeMemgraph is an in-memory Memgraph HA cluster behind the
// memgraph.Connector seam. It keeps one shared SHOW INSTANCES view, applies
// registration commands to it, and — like the real thing — rejects duplicate
// registrations and second MAIN promotions, so any controller behavior that
// is not read-before-write fails the suite loudly.
type fakeMemgraph struct {
	mu sync.Mutex

	// instances is the cluster view every coordinator serves.
	instances []memgraph.Instance
	// staleViews replaces the shared view for individual coordinator addresses:
	// a coordinator that lost the leader answers from its own state machine, so
	// what it reports need not match the cluster at all. Commands still land on
	// the shared view — a stale coordinator is never written to.
	staleViews map[string][]memgraph.Instance
	// behind is how many transactions a data instance trails the MAIN by, for the
	// instances a test puts behind. A registered instance absent from the map is
	// caught up, which is what a healthy cluster looks like — falling behind is the
	// exceptional state a test opts into.
	behind          map[string]int64
	connectAttempts int
	// tlsConnectAttempts counts the connects the operator asked to be over
	// TLS, which is the whole of what the fake can say about the mode: it
	// speaks neither.
	tlsConnectAttempts int
	// connectErr, when set, makes every Connect fail — the operator's view of a
	// cluster whose coordinators do not yet answer Bolt.
	connectErr error
	// unreachable are the Bolt addresses whose Connect fails on their own: a
	// pod that is ready but whose Bolt endpoint is not answering yet.
	unreachable map[string]bool
	// settings is every instance's run-time settings, keyed by the pod Bolt
	// address the operator dials it at. An address not yet in the map answers
	// with baselineSettings, which is what an instance started on the
	// operator's default flag file reports — so a cluster without spec.flags
	// has nothing to SET, and a test that wants an instance out of line puts
	// it there.
	settings map[string]map[string]string
	// coordinatorSettings is the cluster-wide view every coordinator relays
	// from the leader, starting from the core's defaults. A test that wants
	// the leader unreachable for them sets coordinatorSettingsUnknown, which
	// makes SHOW answer with no rows the way a real coordinator does.
	coordinatorSettings        map[string]string
	coordinatorSettingsUnknown bool
	// rejected are commands the cluster refuses whatever its state, keyed by
	// command prefix. It stands in for the rejections the operator cannot reason
	// about — a coordinator refusing a registration a healthy one would accept —
	// which is the only way a permanently failing plan can be provoked here: every
	// other rejection this fake models is one the planner is careful never to plan.
	rejected map[string]error
	// executed records every mutating command as "<bolt address>: <command>".
	executed []string
}

func newFakeMemgraph() *fakeMemgraph {
	return &fakeMemgraph{}
}

func (f *fakeMemgraph) Connect(_ context.Context, address string, tls bool) (memgraph.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectAttempts++
	if tls {
		f.tlsConnectAttempts++
	}
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	if f.unreachable[address] {
		return nil, fmt.Errorf("fake memgraph: %s refused the connection", address)
	}
	return &fakeClient{cluster: f, address: address}, nil
}

// setUnreachable makes every Connect to the given Bolt address fail, or
// succeed again.
func (f *fakeMemgraph) setUnreachable(address string, unreachable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unreachable == nil {
		f.unreachable = map[string]bool{}
	}
	f.unreachable[address] = unreachable
}

// baselineCoordinatorSettings is the SHOW COORDINATOR SETTINGS view of a
// cluster nobody has changed a setting on: Memgraph 3.13.0's defaults.
func baselineCoordinatorSettings() map[string]string {
	return map[string]string{
		"enabled_reads_on_main":               string(settingOff),
		"sync_failover_only":                  string(settingOn),
		"max_failover_replica_lag":            "10",
		"max_replica_read_lag":                "10",
		"deltas_batch_progress_size":          "1000",
		downTimeoutSetting:                    "5",
		"instance_health_check_frequency_sec": "1",
		globalReadOnly:                        string(settingOff),
	}
}

// coordinatorSettingsView is the cluster-wide coordinator settings as the
// coordinators currently hold them.
func (f *fakeMemgraph) coordinatorSettingsView() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.coordinatorSettingsLocked())
}

// setCoordinatorSettingsUnknown makes every coordinator answer SHOW
// COORDINATOR SETTINGS with no rows, as one does without a ready leader.
func (f *fakeMemgraph) setCoordinatorSettingsUnknown(unknown bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coordinatorSettingsUnknown = unknown
}

// coordinatorSettingsLocked must be called with the cluster lock held.
func (f *fakeMemgraph) coordinatorSettingsLocked() map[string]string {
	if f.coordinatorSettings == nil {
		f.coordinatorSettings = baselineCoordinatorSettings()
	}
	return f.coordinatorSettings
}

// baselineSettings is the SHOW DATABASE SETTINGS view of an instance started
// on the operator's default flag file and nothing else.
func baselineSettings() map[string]string {
	return map[string]string{
		"log.level":                 "TRACE",
		"log.to_stderr":             "true",
		"query.timeout":             "600",
		"storage.snapshot.interval": "300",
	}
}

// settingsOf is the run-time settings the instance at the given address
// reports, which a spec reads back to see what landed.
func (f *fakeMemgraph) settingsOf(address string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.settingsLocked(address))
}

// settingsLocked must be called with the cluster lock held.
func (f *fakeMemgraph) settingsLocked(address string) map[string]string {
	if f.settings == nil {
		f.settings = map[string]map[string]string{}
	}
	if _, ok := f.settings[address]; !ok {
		f.settings[address] = baselineSettings()
	}
	return f.settings[address]
}

func (f *fakeMemgraph) setConnectErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectErr = err
}

// rejectCommand makes every command starting with the given prefix fail with the
// given error, leaving the cluster view untouched.
func (f *fakeMemgraph) rejectCommand(prefix string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejected == nil {
		f.rejected = map[string]error{}
	}
	f.rejected[prefix] = err
}

func (f *fakeMemgraph) connects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectAttempts
}

// tlsConnects is how many connects the operator asked to be over TLS.
func (f *fakeMemgraph) tlsConnects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tlsConnectAttempts
}

func (f *fakeMemgraph) executedCommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.executed)
}

func (f *fakeMemgraph) setInstances(instances []memgraph.Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances = slices.Clone(instances)
}

// setLeader moves Raft leadership onto the named coordinator, which is how a spec
// parks it where a scale-down cannot remove it: on an ordinal the shrink retires.
func (f *fakeMemgraph) setLeader(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, instance := range f.instances {
		if !strings.HasPrefix(instance.Name, "coordinator_") {
			continue
		}
		role := memgraph.RoleFollower
		if instance.Name == name {
			role = memgraph.RoleLeader
		}
		f.instances[i].Role = role
	}
}

// setBehind puts the named data instance the given number of transactions behind
// the MAIN, which is how a spec keeps it from being promoted.
func (f *fakeMemgraph) setBehind(name string, txns int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.behind == nil {
		f.behind = map[string]int64{}
	}
	f.behind[name] = txns
}

// setStaleView makes the coordinator at the given Bolt address answer
// SHOW INSTANCES with its own view instead of the cluster's.
func (f *fakeMemgraph) setStaleView(address string, instances []memgraph.Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staleViews == nil {
		f.staleViews = map[string][]memgraph.Instance{}
	}
	f.staleViews[address] = slices.Clone(instances)
}

type fakeClient struct {
	cluster *fakeMemgraph
	address string
	closed  bool
}

// ShowInstances serves the shared cluster view, plus the connected
// coordinator's own row when the cluster does not know it yet. That row is how
// a real coordinator answers before it is added: its initial Raft configuration
// holds itself alone and it is started as the leader of that one-member
// cluster, so it names itself leader and reports an empty bolt_server until
// ADD COORDINATOR fills the address in. The row is not written into the shared
// view — a coordinator the cluster has lost track of speaks only for itself.
func (c *fakeClient) ShowInstances(context.Context) ([]memgraph.Instance, error) {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	if stale, ok := c.cluster.staleViews[c.address]; ok {
		return slices.Clone(stale), nil
	}
	self, err := c.selfName()
	if err != nil {
		return nil, err
	}
	view := slices.Clone(c.cluster.instances)
	if !c.cluster.hasInstance(self) {
		view = append(view, memgraph.Instance{Name: self, Health: "up", Role: memgraph.RoleLeader})
	}
	return view, nil
}

// ShowReplicationLag answers as the real query does: the counts come from the
// MAIN, so a cluster with no MAIN reports nothing at all rather than failing, and
// the MAIN reports itself at zero behind. Every other registered data instance is
// caught up unless a test put it behind.
func (c *fakeClient) ShowReplicationLag(context.Context) ([]memgraph.ReplicationLag, error) {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	if !slices.ContainsFunc(c.cluster.instances, func(instance memgraph.Instance) bool { return instance.IsMain() }) {
		return nil, nil
	}

	var lag []memgraph.ReplicationLag
	for _, instance := range c.cluster.instances {
		if strings.HasPrefix(instance.Name, "coordinator_") {
			continue
		}
		// Lag is measured against the MAIN, so the MAIN is zero behind itself
		// whatever a test set for it — what it set describes the instance as a
		// replica, which is what it becomes once demoted.
		behind := c.cluster.behind[instance.Name]
		if instance.IsMain() {
			behind = 0
		}
		lag = append(lag, memgraph.ReplicationLag{
			Instance: instance.Name,
			Databases: []memgraph.DatabaseLag{{
				Database:       memgraphDbName,
				CommittedTxns:  100 - behind,
				TxnsBehindMain: behind,
			}},
		})
	}
	return lag, nil
}

func (c *fakeClient) AddCoordinator(_ context.Context, coordinator memgraph.CoordinatorSpec) error {
	return c.execute(fmt.Sprintf("ADD COORDINATOR %d", coordinator.ID), func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != coordinator.Name() {
				continue
			}
			if instance.BoltServer != "" {
				return fmt.Errorf("fake memgraph: coordinator %s already exists", coordinator.Name())
			}
			c.cluster.instances[i].BoltServer = coordinator.BoltServer
			c.cluster.instances[i].CoordinatorServer = coordinator.CoordinatorServer
			c.cluster.instances[i].ManagementServer = coordinator.ManagementServer
			return nil
		}
		// Adding the coordinator that is serving this connection materializes
		// the row it has been reporting for itself, so it keeps its leadership;
		// any other coordinator joins the formed cluster as a follower.
		self, err := c.selfName()
		if err != nil {
			return err
		}
		role := memgraph.RoleFollower
		if coordinator.Name() == self {
			role = memgraph.RoleLeader
		}
		c.cluster.instances = append(c.cluster.instances, memgraph.Instance{
			Name:              coordinator.Name(),
			BoltServer:        coordinator.BoltServer,
			CoordinatorServer: coordinator.CoordinatorServer,
			ManagementServer:  coordinator.ManagementServer,
			Health:            "up",
			Role:              role,
		})
		return nil
	})
}

func (c *fakeClient) RegisterInstance(_ context.Context, instance memgraph.DataInstanceSpec) error {
	return c.execute("REGISTER INSTANCE "+instance.Name, func() error {
		if c.cluster.hasInstance(instance.Name) {
			return fmt.Errorf("fake memgraph: instance %s already registered", instance.Name)
		}
		c.cluster.instances = append(c.cluster.instances, memgraph.Instance{
			Name:             instance.Name,
			BoltServer:       instance.BoltServer,
			ManagementServer: instance.ManagementServer,
			Health:           "up",
			Role:             memgraph.RoleReplica,
		})
		return nil
	})
}

// UpdateCoordinatorBoltServer and UpdateInstanceBoltServer move a registered
// member's announced bolt address, and — like the real thing — refuse a member
// the cluster does not know, with no precondition on its health.
func (c *fakeClient) UpdateCoordinatorBoltServer(_ context.Context, id int32, boltServer string) error {
	name := fmt.Sprintf("coordinator_%d", id)
	return c.execute(fmt.Sprintf("UPDATE CONFIG FOR COORDINATOR %d bolt_server=%s", id, boltServer), func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name == name && instance.BoltServer != "" {
				c.cluster.instances[i].BoltServer = boltServer
				return nil
			}
		}
		return fmt.Errorf("fake memgraph: coordinator %s is not a member", name)
	})
}

func (c *fakeClient) UpdateInstanceBoltServer(_ context.Context, name, boltServer string) error {
	return c.execute(fmt.Sprintf("UPDATE CONFIG FOR INSTANCE %s bolt_server=%s", name, boltServer), func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name == name {
				c.cluster.instances[i].BoltServer = boltServer
				return nil
			}
		}
		return fmt.Errorf("fake memgraph: instance %s is not registered", name)
	})
}

func (c *fakeClient) SetInstanceToMain(_ context.Context, name string) error {
	return c.execute(fmt.Sprintf("SET INSTANCE %s TO MAIN", name), func() error {
		for _, instance := range c.cluster.instances {
			if instance.IsMain() {
				return fmt.Errorf("fake memgraph: %s is already MAIN", instance.Name)
			}
		}
		for i, instance := range c.cluster.instances {
			if instance.Name == name {
				c.cluster.instances[i].Role = memgraph.RoleMain
				return nil
			}
		}
		return fmt.Errorf("fake memgraph: instance %s is not registered", name)
	})
}

// DemoteInstance turns the named MAIN back into a replica, and — like the real
// thing — refuses an instance that is not MAIN, so the operator's read-before-write
// is what has to keep this call meaningful.
func (c *fakeClient) DemoteInstance(_ context.Context, name string) error {
	return c.execute("DEMOTE INSTANCE "+name, func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != name {
				continue
			}
			if !instance.IsMain() {
				return fmt.Errorf("fake memgraph: instance %s is not MAIN", name)
			}
			c.cluster.instances[i].Role = memgraph.RoleReplica
			return nil
		}
		return fmt.Errorf("fake memgraph: instance %s is not registered", name)
	})
}

// UnregisterInstance removes the named data instance from the cluster view. It
// rejects an unregistered name and, as Memgraph does, the MAIN — so a plan that
// aims an unregistration at a MAIN fails the suite loudly.
func (c *fakeClient) UnregisterInstance(_ context.Context, name string) error {
	return c.execute("UNREGISTER INSTANCE "+name, func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != name {
				continue
			}
			if instance.IsMain() {
				return fmt.Errorf("fake memgraph: instance %s is MAIN", name)
			}
			c.cluster.instances = slices.Delete(c.cluster.instances, i, i+1)
			return nil
		}
		return fmt.Errorf("fake memgraph: instance %s is not registered", name)
	})
}

// RemoveCoordinator drops the coordinator with the given Raft ID from the cluster
// view and — as Raft does — refuses the current leader, so a plan that aims a
// removal at the leader fails the suite loudly instead of quietly working.
func (c *fakeClient) RemoveCoordinator(_ context.Context, id int32) error {
	name := fmt.Sprintf("coordinator_%d", id)
	return c.execute(fmt.Sprintf("REMOVE COORDINATOR %d", id), func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != name {
				continue
			}
			if instance.IsLeader() {
				return fmt.Errorf("fake memgraph: %s is the leader", name)
			}
			c.cluster.instances = slices.Delete(c.cluster.instances, i, i+1)
			return nil
		}
		return fmt.Errorf("fake memgraph: coordinator %s is not a member", name)
	})
}

// YieldLeadership moves leadership off the coordinator serving this connection to
// the lowest-numbered remaining member, standing in for the election NuRaft runs.
// A test cannot rely on which coordinator wins — that is the point of the command
// — only on leadership having moved, which is what the operator has to converge
// around.
func (c *fakeClient) YieldLeadership(context.Context) error {
	self, err := c.selfName()
	if err != nil {
		return err
	}
	return c.execute("YIELD LEADERSHIP", func() error {
		successor := -1
		for i, instance := range c.cluster.instances {
			if strings.HasPrefix(instance.Name, "coordinator_") && instance.Name != self {
				successor = i
				break
			}
		}
		if successor < 0 {
			return fmt.Errorf("fake memgraph: %s is the only coordinator, so leadership cannot be yielded", self)
		}
		for i, instance := range c.cluster.instances {
			if instance.Name == self {
				c.cluster.instances[i].Role = memgraph.RoleFollower
			}
		}
		c.cluster.instances[successor].Role = memgraph.RoleLeader
		return nil
	})
}

// ShowSettings answers for the connected instance alone, as the real thing
// does: a run-time setting is local to the instance that holds it.
func (c *fakeClient) ShowSettings(context.Context) (map[string]string, error) {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	return maps.Clone(c.cluster.settingsLocked(c.address)), nil
}

// SetSetting changes one setting on the connected instance. Like Memgraph it
// refuses a name it does not know — the baseline is the whole of what it
// knows — and a boolean setting's value that is not true or false.
func (c *fakeClient) SetSetting(_ context.Context, name, value string) error {
	command := fmt.Sprintf("SET DATABASE SETTING %q TO %q", name, value)
	return c.execute(command, func() error {
		settings := c.cluster.settingsLocked(c.address)
		if _, ok := settings[name]; !ok {
			return fmt.Errorf("fake memgraph: Unknown setting name '%s'", name)
		}
		if name == "log.to_stderr" && value != string(flagOn) && value != string(flagOff) {
			return fmt.Errorf("fake memgraph: Cannot update setting '%s': "+
				"Boolean value supports only 'false' or 'true' as the input.", name)
		}
		settings[name] = value
		return nil
	})
}

// ShowCoordinatorSettings relays the cluster-wide view, or nothing when the
// leader is unreachable, as the real thing does.
func (c *fakeClient) ShowCoordinatorSettings(context.Context) (map[string]string, error) {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	if c.cluster.coordinatorSettingsUnknown {
		return map[string]string{}, nil
	}
	return maps.Clone(c.cluster.coordinatorSettingsLocked()), nil
}

// SetCoordinatorSetting writes one cluster-wide setting, on whichever
// coordinator it arrives at. Like Memgraph it refuses a setting it does not
// have and a boolean that is not true or false.
func (c *fakeClient) SetCoordinatorSetting(_ context.Context, name, value string) error {
	command := fmt.Sprintf("SET COORDINATOR SETTING %q TO %q", name, value)
	return c.execute(command, func() error {
		settings := c.cluster.coordinatorSettingsLocked()
		if _, ok := settings[name]; !ok {
			return fmt.Errorf("fake memgraph: Setting %s doesn't exist on coordinators.", name)
		}
		if name == globalReadOnly && value != string(settingOn) && value != string(settingOff) {
			return fmt.Errorf("fake memgraph: Invalid argument detected while trying to update setting %s", name)
		}
		settings[name] = value
		return nil
	})
}

func (c *fakeClient) Close(context.Context) error {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	c.closed = true
	return nil
}

// execute records the command and applies it to the shared cluster view.
func (c *fakeClient) execute(command string, apply func() error) error {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	for prefix, err := range c.cluster.rejected {
		if strings.HasPrefix(command, prefix) {
			return err
		}
	}
	if err := apply(); err != nil {
		return err
	}
	c.cluster.executed = append(c.cluster.executed, c.address+": "+command)
	return nil
}

// selfName is the instance name of the coordinator this connection is served
// by. Addresses are the resource builders' pod FQDNs
// ("<statefulset>-<ordinal>.<service>.<namespace>.svc.<domain>:<port>") and the
// coordinator on pod ordinal N runs with Raft ID N.
func (c *fakeClient) selfName() (string, error) {
	pod, _, _ := strings.Cut(c.address, ".")
	dash := strings.LastIndex(pod, "-")
	if dash < 0 {
		return "", fmt.Errorf("fake memgraph: %s is not a pod address", c.address)
	}
	ordinal, err := strconv.Atoi(pod[dash+1:])
	if err != nil {
		return "", fmt.Errorf("fake memgraph: %s carries no pod ordinal: %w", c.address, err)
	}
	return fmt.Sprintf("coordinator_%d", ordinal), nil
}

// hasInstance must be called with the cluster lock held.
func (f *fakeMemgraph) hasInstance(name string) bool {
	return slices.ContainsFunc(f.instances, func(instance memgraph.Instance) bool {
		return instance.Name == name
	})
}

// view is the cluster view as the coordinators currently hold it: what a
// spec reads back to see where a member is announced.
func (f *fakeMemgraph) view() []memgraph.Instance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.instances)
}
