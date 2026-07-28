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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/db"
)

const testInstanceName = "instance_1"

func TestAddCoordinatorQuery(t *testing.T) {
	got := addCoordinatorQuery(CoordinatorSpec{
		ID:                2,
		BoltServer:        "example-coordinator-1.example-coordinator.default.svc.cluster.local:7687",
		CoordinatorServer: "example-coordinator-1.example-coordinator.default.svc.cluster.local:12000",
		ManagementServer:  "example-coordinator-1.example-coordinator.default.svc.cluster.local:10000",
	})
	want := `ADD COORDINATOR 2 WITH CONFIG {` +
		`"bolt_server": "example-coordinator-1.example-coordinator.default.svc.cluster.local:7687", ` +
		`"coordinator_server": "example-coordinator-1.example-coordinator.default.svc.cluster.local:12000", ` +
		`"management_server": "example-coordinator-1.example-coordinator.default.svc.cluster.local:10000"}`
	if got != want {
		t.Errorf("addCoordinatorQuery() = %q, want %q", got, want)
	}
}

func TestRegisterInstanceQuery(t *testing.T) {
	got := registerInstanceQuery(DataInstanceSpec{
		Name:              testInstanceName,
		BoltServer:        "example-data-0.example-data.default.svc.cluster.local:7687",
		ManagementServer:  "example-data-0.example-data.default.svc.cluster.local:10000",
		ReplicationServer: "example-data-0.example-data.default.svc.cluster.local:20000",
	})
	want := `REGISTER INSTANCE instance_1 WITH CONFIG {` +
		`"bolt_server": "example-data-0.example-data.default.svc.cluster.local:7687", ` +
		`"management_server": "example-data-0.example-data.default.svc.cluster.local:10000", ` +
		`"replication_server": "example-data-0.example-data.default.svc.cluster.local:20000"}`
	if got != want {
		t.Errorf("registerInstanceQuery() = %q, want %q", got, want)
	}
}

func TestSetInstanceToMainQuery(t *testing.T) {
	got := setInstanceToMainQuery(testInstanceName)
	if want := "SET INSTANCE instance_1 TO MAIN"; got != want {
		t.Errorf("setInstanceToMainQuery() = %q, want %q", got, want)
	}
}

func TestDemoteInstanceQuery(t *testing.T) {
	got := demoteInstanceQuery(testInstanceName)
	if want := "DEMOTE INSTANCE instance_1"; got != want {
		t.Errorf("demoteInstanceQuery() = %q, want %q", got, want)
	}
}

func TestUnregisterInstanceQuery(t *testing.T) {
	got := unregisterInstanceQuery(testInstanceName)
	if want := "UNREGISTER INSTANCE instance_1"; got != want {
		t.Errorf("unregisterInstanceQuery() = %q, want %q", got, want)
	}
}

func TestRemoveCoordinatorQuery(t *testing.T) {
	got := removeCoordinatorQuery(4)
	if want := "REMOVE COORDINATOR 4"; got != want {
		t.Errorf("removeCoordinatorQuery() = %q, want %q", got, want)
	}
}

// YIELD LEADERSHIP names no successor: the coordinator it runs on is the subject,
// and NuRaft picks who takes over. A query that grew an argument would mean the
// planner could suddenly predict the outcome, so the shape is pinned.
func TestYieldLeadershipQuery(t *testing.T) {
	if want := "YIELD LEADERSHIP"; yieldLeadershipQuery != want {
		t.Errorf("yieldLeadershipQuery = %q, want %q", yieldLeadershipQuery, want)
	}
}

func TestInstanceFromRecord(t *testing.T) {
	record := &db.Record{
		Keys: []string{
			"name", "bolt_server", "coordinator_server", "management_server", "health", "role", "last_succ_resp_ms",
		},
		Values: []any{
			"coordinator_1", "localhost:7687", "localhost:12000", "localhost:10000", "up", "leader", int64(12),
		},
	}
	want := Instance{
		Name:              "coordinator_1",
		BoltServer:        "localhost:7687",
		CoordinatorServer: "localhost:12000",
		ManagementServer:  "localhost:10000",
		Health:            "up",
		Role:              "leader",
	}
	if diff := cmp.Diff(want, instanceFromRecord(record)); diff != "" {
		t.Errorf("instanceFromRecord() mismatch (-want +got):\n%s", diff)
	}
}

func TestInstanceFromRecordToleratesMissingColumns(t *testing.T) {
	record := &db.Record{
		Keys:   []string{"name", "role"},
		Values: []any{testInstanceName, "main"},
	}
	want := Instance{Name: testInstanceName, Role: "main"}
	if diff := cmp.Diff(want, instanceFromRecord(record)); diff != "" {
		t.Errorf("instanceFromRecord() mismatch (-want +got):\n%s", diff)
	}
}
