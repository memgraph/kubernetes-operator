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
