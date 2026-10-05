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

package settings

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The flag and setting names the cases below share, so each is spelled once.
const (
	logLevelFlag      = "log-level"
	logLevelCanonical = "log_level"
	snapshotFlag      = "storage-snapshot-interval"
	hopsLimit         = "hops_limit_partial_results"
	downTimeout       = "instance_down_timeout_sec"
	readsOnMain       = "enabled_reads_on_main"
	stderrFlag        = "also_log_to_stderr"
	schedulerName     = "scheduler"
	snapshotOnExit    = "storage_snapshot_on_exit"
	infoValue         = "INFO"
	on                = "true"
	off               = "false"

	logLevel         = "log.level"
	logToStderr      = "log.to_stderr"
	queryTimeout     = "query.timeout"
	snapshotInterval = "storage.snapshot.interval"
	gcAggressive     = "storage-gc-aggressive"
	cartesianProduct = "cartesian-product-enabled"
	debugQueryPlans  = "debug-query-plans"
	timezone         = "timezone"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		logLevelCanonical:      logLevelCanonical,
		logLevelFlag:           logLevelCanonical,
		"--log-level":          logLevelCanonical,
		"-log_level":           logLevelCanonical,
		"storage-gc-cycle-sec": "storage_gc_cycle_sec",
		"":                     "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSettingTable pins the flag-to-setting table against the names Memgraph
// 3.13.0 declares in src/flags/run_time_configurable.cpp. A flag reaching a
// setting under the wrong name would be a SET the instance rejects forever,
// so every entry is spelled out here rather than derived.
func TestSettingTable(t *testing.T) {
	want := map[string]string{
		"bolt-server-name-for-init":   "server.name",
		"query-execution-timeout-sec": queryTimeout,
		"hops-limit-partial-results":  hopsLimit,
		logLevelFlag:                  logLevel,
		"also-log-to-stderr":          logToStderr,
		cartesianProduct:              cartesianProduct,
		debugQueryPlans:               debugQueryPlans,
		gcAggressive:                  gcAggressive,
		"storage-omit-vector-index-properties-on-return": "storage.omit_vector_index_properties_on_return",
		timezone:                         timezone,
		snapshotFlag:                     snapshotInterval,
		"file-download-conn-timeout-sec": "file.download_conn_timeout_sec",
		"storage-access-timeout-sec":     "storage.access_timeout_sec",
		"log-min-duration-ms":            "log.min_duration_ms",
		"log-failed-queries":             "log.failed_queries",
		"log-query-plan":                 "log.query_plan",
	}
	if len(runtimeSettings) != len(want) {
		t.Errorf("table has %d entries, this test pins %d", len(runtimeSettings), len(want))
	}
	for flag, setting := range want {
		got, ok := Setting(flag)
		if !ok || got != setting {
			t.Errorf("Setting(%q) = %q, %v; want %q, true", flag, got, ok, setting)
		}
	}
	for _, flag := range []string{
		"storage-snapshot-on-exit", "memory-limit", "log-retention-days", "bolt-port",
		// The awsCredentials Secret's: admission rejects them, and the table
		// does not know them either, so a spec that slipped past admission
		// would still never see them SET from the flags.
		"aws-access-key", "aws-secret-key", "aws-region", "aws-endpoint-url",
	} {
		if setting, ok := Setting(flag); ok {
			t.Errorf("Setting(%q) = %q, want a startup-only flag", flag, setting)
		}
	}
}

func TestPlan(t *testing.T) {
	observed := map[string]string{
		logLevel:         "TRACE",
		logToStderr:      on,
		queryTimeout:     "600",
		snapshotInterval: "300",
	}

	for _, tc := range []struct {
		name  string
		flags map[string]string
		want  []Change
	}{
		{
			name: "converged flags plan nothing",
			flags: map[string]string{
				logLevelCanonical: "TRACE", "also_log_to_stderr": on, "log_retention_days": "35",
			},
		},
		{
			name:  "a changed run-time flag is planned under its setting name",
			flags: map[string]string{logLevelFlag: "INFO"},
			want:  []Change{{Setting: logLevel, Value: "INFO"}},
		},
		{
			name: "changes come out ordered by setting name whatever the map order",
			flags: map[string]string{
				snapshotFlag:                  "60",
				"query-execution-timeout-sec": "10",
				logLevelCanonical:             "WARNING",
			},
			want: []Change{
				{Setting: logLevel, Value: "WARNING"},
				{Setting: queryTimeout, Value: "10"},
				{Setting: snapshotInterval, Value: "60"},
			},
		},
		{
			name:  "a startup-only flag is never planned",
			flags: map[string]string{"storage-snapshot-on-exit": off, "memory-limit": "2048"},
		},
		{
			name:  "a setting the instance does not report is planned, so an old image surfaces as a rejection",
			flags: map[string]string{timezone: "Europe/Zagreb"},
			want:  []Change{{Setting: timezone, Value: "Europe/Zagreb"}},
		},
		{
			name:  "an empty value is a value",
			flags: map[string]string{snapshotFlag: ""},
			want:  []Change{{Setting: snapshotInterval, Value: ""}},
		},
		{
			name:  "a flag absent from the map leaves the instance's setting alone",
			flags: map[string]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Plan(tc.flags, observed)); diff != "" {
				t.Errorf("Plan mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDiff pins the rules Plan and the coordinator settings share, without
// the flag table in the way: desired keys only, sorted, an unreported setting
// planned, an absent key left alone.
func TestDiff(t *testing.T) {
	observed := map[string]string{
		readsOnMain:        off,
		downTimeout:        "5",
		"global_read_only": off,
	}
	for _, tc := range []struct {
		name    string
		desired map[string]string
		want    []Change
	}{
		{name: "nothing desired plans nothing"},
		{name: "a matching value plans nothing", desired: map[string]string{downTimeout: "5"}},
		{
			name:    "differing values come out by name",
			desired: map[string]string{downTimeout: "7", readsOnMain: on},
			want: []Change{
				{Setting: readsOnMain, Value: on},
				{Setting: downTimeout, Value: "7"},
			},
		},
		{
			name:    "an unreported setting is planned, so the coordinators get to refuse it",
			desired: map[string]string{"no_such_setting": "1"},
			want:    []Change{{Setting: "no_such_setting", Value: "1"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Diff(tc.desired, observed)); diff != "" {
				t.Errorf("Diff mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassify pins how a role's keys are sorted against the two views a
// coordinator answers with, in either spelling, and that the hidden flags
// SHOW CONFIG leaves out still count as flags.
func TestClassify(t *testing.T) {
	config := map[string]string{logLevelCanonical: infoValue, snapshotOnExit: "false", "memory_limit": "0"}
	coordinatorSettings := map[string]string{readsOnMain: off, downTimeout: "5"}

	got := Classify([]string{
		"log-level", snapshotOnExit, "also-log-to-stderr", schedulerName,
		"enabled-reads-on-main", downTimeout,
		"enabled_reads_on_mai", "memory-limti",
	}, config, coordinatorSettings)
	want := Classification{
		Flags:               []string{stderrFlag, logLevelCanonical, schedulerName, snapshotOnExit},
		CoordinatorSettings: []string{readsOnMain, downTimeout},
		Unknown:             []string{"enabled_reads_on_mai", "memory_limti"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Classify mismatch (-want +got):\n%s", diff)
	}

	// With no config view nothing is a flag: the caller must read that as
	// "cannot tell", which is why it is pinned here rather than guessed at.
	empty := Classify([]string{logLevelCanonical, readsOnMain}, nil, coordinatorSettings)
	if len(empty.Flags) != 0 || len(empty.Unknown) != 1 || len(empty.CoordinatorSettings) != 1 {
		t.Errorf("Classify with no config view = %+v, want every non-coordinator key unknown", empty)
	}
}
