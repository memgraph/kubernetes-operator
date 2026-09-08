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

package memgraph

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/db"
)

// NewBoltConnector returns the production Connector dialing coordinators over
// unauthenticated Bolt (Bolt auth is out of scope for v1alpha1).
func NewBoltConnector() Connector {
	return boltConnector{}
}

type boltConnector struct{}

func (boltConnector) Connect(ctx context.Context, address string) (Client, error) {
	driver, err := neo4j.NewDriverWithContext("bolt://"+address, neo4j.NoAuth())
	if err != nil {
		return nil, fmt.Errorf("creating bolt driver for %s: %w", address, err)
	}
	if err := driver.VerifyConnectivity(ctx); err != nil {
		_ = driver.Close(ctx)
		return nil, fmt.Errorf("connecting to %s: %w", address, err)
	}
	return &boltClient{driver: driver}, nil
}

type boltClient struct {
	driver neo4j.DriverWithContext
}

func (c *boltClient) ShowInstances(ctx context.Context) ([]Instance, error) {
	records, err := c.run(ctx, showInstancesQuery)
	if err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(records))
	for _, record := range records {
		instances = append(instances, instanceFromRecord(record))
	}
	return instances, nil
}

func (c *boltClient) ShowReplicationLag(ctx context.Context) ([]ReplicationLag, error) {
	records, err := c.run(ctx, showReplicationLagQuery)
	if err != nil {
		return nil, err
	}
	lag := make([]ReplicationLag, 0, len(records))
	for _, record := range records {
		instance, err := replicationLagFromRecord(record)
		if err != nil {
			return nil, err
		}
		lag = append(lag, instance)
	}
	return lag, nil
}

func (c *boltClient) AddCoordinator(ctx context.Context, coordinator CoordinatorSpec) error {
	_, err := c.run(ctx, addCoordinatorQuery(coordinator))
	return err
}

func (c *boltClient) RegisterInstance(ctx context.Context, instance DataInstanceSpec) error {
	_, err := c.run(ctx, registerInstanceQuery(instance))
	return err
}

func (c *boltClient) SetInstanceToMain(ctx context.Context, name string) error {
	_, err := c.run(ctx, setInstanceToMainQuery(name))
	return err
}

func (c *boltClient) DemoteInstance(ctx context.Context, name string) error {
	_, err := c.run(ctx, demoteInstanceQuery(name))
	return err
}

func (c *boltClient) UnregisterInstance(ctx context.Context, name string) error {
	_, err := c.run(ctx, unregisterInstanceQuery(name))
	return err
}

func (c *boltClient) RemoveCoordinator(ctx context.Context, id int32) error {
	_, err := c.run(ctx, removeCoordinatorQuery(id))
	return err
}

func (c *boltClient) YieldLeadership(ctx context.Context) error {
	_, err := c.run(ctx, yieldLeadershipQuery)
	return err
}

func (c *boltClient) Close(ctx context.Context) error {
	return c.driver.Close(ctx)
}

// run executes one query in an autocommit session; Memgraph's coordinator
// queries cannot run inside explicit transactions.
func (c *boltClient) run(ctx context.Context, query string) ([]*db.Record, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{})
	defer func() { _ = session.Close(ctx) }()

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("running %q: %w", query, err)
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return nil, fmt.Errorf("collecting results of %q: %w", query, err)
	}
	return records, nil
}

// instanceFromRecord maps one SHOW INSTANCES row to an Instance. Columns are
// looked up by name so the parsing survives added or reordered columns
// (last_succ_resp_ms is deliberately ignored).
func instanceFromRecord(record *db.Record) Instance {
	return Instance{
		Name:              stringColumn(record, "name"),
		BoltServer:        stringColumn(record, "bolt_server"),
		CoordinatorServer: stringColumn(record, "coordinator_server"),
		ManagementServer:  stringColumn(record, "management_server"),
		Health:            stringColumn(record, "health"),
		Role:              stringColumn(record, "role"),
	}
}

// replicationLagFromRecord maps one SHOW REPLICATION LAG row to a
// ReplicationLag: an instance_name and a data_info map keyed by database name,
// each entry carrying that database's counters.
//
// Unlike instanceFromRecord this refuses a row it cannot read rather than
// filling in zero values. The leniency there is safe because a missing column
// leaves an empty string, which reads as neither up nor MAIN; here a missing
// counter would read as zero transactions behind, which is precisely the answer
// that makes an instance promotable. This view must never invent that.
func replicationLagFromRecord(record *db.Record) (ReplicationLag, error) {
	name := stringColumn(record, "instance_name")
	if name == "" {
		return ReplicationLag{}, fmt.Errorf("%s row carries no instance_name", showReplicationLagQuery)
	}
	databases, ok := mapColumn(record, "data_info")
	if !ok {
		return ReplicationLag{}, fmt.Errorf("%s row for %s carries no data_info map", showReplicationLagQuery, name)
	}

	lag := ReplicationLag{Instance: name}
	// Databases come out ordered by name so the view a caller compares is stable
	// across reconciles; the driver hands back an unordered map.
	for _, database := range slices.Sorted(maps.Keys(databases)) {
		counters, ok := asMap(databases[database])
		if !ok {
			return ReplicationLag{}, fmt.Errorf("%s row for %s carries no counters for database %s",
				showReplicationLagQuery, name, database)
		}
		committed, ok := intEntry(counters, "num_committed_txns")
		if !ok {
			return ReplicationLag{}, fmt.Errorf("%s row for %s is missing num_committed_txns for database %s",
				showReplicationLagQuery, name, database)
		}
		behind, ok := intEntry(counters, "num_txns_behind_main")
		if !ok {
			return ReplicationLag{}, fmt.Errorf("%s row for %s is missing num_txns_behind_main for database %s",
				showReplicationLagQuery, name, database)
		}
		lag.Databases = append(lag.Databases, DatabaseLag{
			Database:       database,
			CommittedTxns:  committed,
			TxnsBehindMain: behind,
		})
	}
	return lag, nil
}

func stringColumn(record *db.Record, key string) string {
	value, ok := record.Get(key)
	if !ok {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return s
}

func mapColumn(record *db.Record, key string) (map[string]any, bool) {
	value, ok := record.Get(key)
	if !ok {
		return nil, false
	}
	return asMap(value)
}

func asMap(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func intEntry(m map[string]any, key string) (int64, bool) {
	value, ok := m[key]
	if !ok {
		return 0, false
	}
	i, ok := value.(int64)
	return i, ok
}
