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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/db"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

const testInstanceName = "instance_1"

// The SHOW REPLICATION LAG column and map keys, plus the two databases the cases
// below report on. They are spelled out here rather than shared with the parser
// on purpose: pinning the names the parser reads off the wire is what these tests
// are for, and a constant shared with it would let a rename pass unnoticed.
const (
	instanceNameColumn = "instance_name"
	dataInfoColumn     = "data_info"
	committedTxnsKey   = "num_committed_txns"
	behindMainKey      = "num_txns_behind_main"
	defaultDatabase    = "memgraph"
	otherDatabase      = "analytics"
)

func TestAddCoordinatorQuery(t *testing.T) {
	host := "example-coordinator-1.example-coordinator.default.svc.cluster.local"
	got := addCoordinatorQuery(CoordinatorSpec{
		ID:                2,
		BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
		CoordinatorServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.CoordinatorPort),
		ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
	})
	want := fmt.Sprintf(`ADD COORDINATOR 2 WITH CONFIG {`+
		`"bolt_server": "%s:%d", `+
		`"coordinator_server": "%s:%d", `+
		`"management_server": "%s:%d"}`,
		host, memgraphcomv1alpha1.BoltPort,
		host, memgraphcomv1alpha1.CoordinatorPort,
		host, memgraphcomv1alpha1.ManagementPort)
	if got != want {
		t.Errorf("addCoordinatorQuery() = %q, want %q", got, want)
	}
}

func TestRegisterInstanceQuery(t *testing.T) {
	host := "example-data-0.example-data.default.svc.cluster.local"
	got := registerInstanceQuery(DataInstanceSpec{
		Name:              testInstanceName,
		BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
		ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
		ReplicationServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ReplicationPort),
	})
	want := fmt.Sprintf(`REGISTER INSTANCE instance_1 WITH CONFIG {`+
		`"bolt_server": "%s:%d", `+
		`"management_server": "%s:%d", `+
		`"replication_server": "%s:%d"}`,
		host, memgraphcomv1alpha1.BoltPort,
		host, memgraphcomv1alpha1.ManagementPort,
		host, memgraphcomv1alpha1.ReplicationPort)
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

// SHOW REPLICATION LAG takes no argument: one call covers every instance the
// cluster knows, so a query that grew one would mean the caller had to ask per
// instance and could no longer compare them from a single view.
func TestShowReplicationLagQuery(t *testing.T) {
	if want := "SHOW REPLICATION LAG"; showReplicationLagQuery != want {
		t.Errorf("showReplicationLagQuery = %q, want %q", showReplicationLagQuery, want)
	}
}

func TestReplicationLagFromRecord(t *testing.T) {
	record := &db.Record{
		Keys: []string{instanceNameColumn, dataInfoColumn},
		Values: []any{testInstanceName, map[string]any{
			defaultDatabase: map[string]any{
				committedTxnsKey: int64(42),
				behindMainKey:    int64(0),
			},
			// Sorted by database name, so this one comes out first.
			otherDatabase: map[string]any{
				committedTxnsKey: int64(40),
				behindMainKey:    int64(2),
			},
		}},
	}
	want := ReplicationLag{
		Instance: testInstanceName,
		Databases: []DatabaseLag{
			{Database: otherDatabase, CommittedTxns: 40, TxnsBehindMain: 2},
			{Database: defaultDatabase, CommittedTxns: 42, TxnsBehindMain: 0},
		},
	}

	got, err := replicationLagFromRecord(record)
	if err != nil {
		t.Fatalf("replicationLagFromRecord() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("replicationLagFromRecord() mismatch (-want +got):\n%s", diff)
	}
}

// A row the parsing cannot read has to fail rather than default. Every field it
// could default is zero, and zero transactions behind is exactly the answer that
// makes an instance promotable — so a malformed row must never be quietly read as
// a caught-up one.
func TestReplicationLagFromRecordRejectsUnreadableRows(t *testing.T) {
	cases := []struct {
		name   string
		record *db.Record
	}{
		{
			name:   "no instance_name",
			record: &db.Record{Keys: []string{dataInfoColumn}, Values: []any{map[string]any{}}},
		},
		{
			name:   "no data_info",
			record: &db.Record{Keys: []string{instanceNameColumn}, Values: []any{testInstanceName}},
		},
		{
			name: "data_info is not a map of maps",
			record: &db.Record{
				Keys:   []string{instanceNameColumn, dataInfoColumn},
				Values: []any{testInstanceName, map[string]any{defaultDatabase: int64(3)}},
			},
		},
		{
			name: "no num_txns_behind_main",
			record: &db.Record{
				Keys: []string{instanceNameColumn, dataInfoColumn},
				Values: []any{testInstanceName, map[string]any{
					defaultDatabase: map[string]any{committedTxnsKey: int64(42)},
				}},
			},
		},
		{
			name: "no num_committed_txns",
			record: &db.Record{
				Keys: []string{instanceNameColumn, dataInfoColumn},
				Values: []any{testInstanceName, map[string]any{
					defaultDatabase: map[string]any{behindMainKey: int64(0)},
				}},
			},
		},
		{
			name: "counters are not integers",
			record: &db.Record{
				Keys: []string{instanceNameColumn, dataInfoColumn},
				Values: []any{testInstanceName, map[string]any{
					defaultDatabase: map[string]any{committedTxnsKey: "42", behindMainKey: "0"},
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := replicationLagFromRecord(tc.record); err == nil {
				t.Error("replicationLagFromRecord() succeeded on an unreadable row, want error")
			}
		})
	}
}

func TestReplicationLagIsCaughtUp(t *testing.T) {
	cases := []struct {
		name string
		lag  ReplicationLag
		want bool
	}{
		{
			name: "every database at the MAIN's offset",
			lag: ReplicationLag{Instance: testInstanceName, Databases: []DatabaseLag{
				{Database: otherDatabase, TxnsBehindMain: 0},
				{Database: defaultDatabase, TxnsBehindMain: 0},
			}},
			want: true,
		},
		{
			name: "behind in one database of several",
			lag: ReplicationLag{Instance: testInstanceName, Databases: []DatabaseLag{
				{Database: otherDatabase, TxnsBehindMain: 0},
				{Database: defaultDatabase, TxnsBehindMain: 1},
			}},
			want: false,
		},
		// SYNC replication can leave a replica holding transactions a newly promoted
		// MAIN never saw. Ahead is not behind, so it does not disqualify.
		{
			name: "ahead of the MAIN",
			lag: ReplicationLag{Instance: testInstanceName, Databases: []DatabaseLag{
				{Database: defaultDatabase, TxnsBehindMain: -2},
			}},
			want: true,
		},
		// Nothing reported is not the same as nothing behind: an instance the MAIN
		// does not list is one whose progress is unknown.
		{
			name: "no databases reported",
			lag:  ReplicationLag{Instance: testInstanceName},
			want: false,
		},
		{
			name: "the zero value, as a lookup miss reads back",
			lag:  ReplicationLag{},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.lag.IsCaughtUp(); got != tc.want {
				t.Errorf("IsCaughtUp() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestInstanceFromRecord(t *testing.T) {
	record := &db.Record{
		Keys: []string{
			"name", "bolt_server", "coordinator_server", "management_server", "health", "role", "last_succ_resp_ms",
		},
		Values: []any{
			"coordinator_1",
			fmt.Sprintf("localhost:%d", memgraphcomv1alpha1.BoltPort),
			fmt.Sprintf("localhost:%d", memgraphcomv1alpha1.CoordinatorPort),
			fmt.Sprintf("localhost:%d", memgraphcomv1alpha1.ManagementPort),
			"up", "leader", int64(12),
		},
	}
	want := Instance{
		Name:              "coordinator_1",
		BoltServer:        fmt.Sprintf("localhost:%d", memgraphcomv1alpha1.BoltPort),
		CoordinatorServer: fmt.Sprintf("localhost:%d", memgraphcomv1alpha1.CoordinatorPort),
		ManagementServer:  fmt.Sprintf("localhost:%d", memgraphcomv1alpha1.ManagementPort),
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
