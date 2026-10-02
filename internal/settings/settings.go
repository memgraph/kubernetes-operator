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

// Package settings is the pure core behind spec.flags and
// spec.coordinatorSettings: which Memgraph flags can be changed on a running
// instance, under which SHOW DATABASE SETTINGS name, and what SET commands
// bring an observed settings view in line with a desired one. Nothing here
// touches Kubernetes or Bolt; the controller feeds it the rendered flag file
// or the coordinator settings block plus the observed view and issues what
// comes back.
package settings

import (
	"slices"
	"strings"
)

// runtimeSettings maps every Memgraph flag that also exists as a run-time
// setting to the name SET DATABASE SETTING and SHOW DATABASE SETTINGS use for
// it, keyed by the flag's canonical gflags spelling (see Normalize). It
// mirrors register_flag in src/flags/run_time_configurable.cpp of the pinned
// Memgraph version, where the two names are declared side by side; a flag
// absent from it is read at startup only, and a change to it is carried by
// a restart. aws_access_key and aws_secret_key are run-time settings too but
// are left out on purpose: admission rejects them in spec.flags, because
// they are secret material.
var runtimeSettings = map[string]string{
	"bolt_server_name_for_init":                      "server.name",
	"query_execution_timeout_sec":                    "query.timeout",
	"hops_limit_partial_results":                     "hops_limit_partial_results",
	"log_level":                                      "log.level",
	"also_log_to_stderr":                             "log.to_stderr",
	"cartesian_product_enabled":                      "cartesian-product-enabled",
	"debug_query_plans":                              "debug-query-plans",
	"storage_gc_aggressive":                          "storage-gc-aggressive",
	"storage_omit_vector_index_properties_on_return": "storage.omit_vector_index_properties_on_return",
	"timezone":                                       "timezone",
	"storage_snapshot_interval":                      "storage.snapshot.interval",
	"aws_region":                                     "aws.region",
	"aws_endpoint_url":                               "aws.endpoint_url",
	"file_download_conn_timeout_sec":                 "file.download_conn_timeout_sec",
	"storage_access_timeout_sec":                     "storage.access_timeout_sec",
	"log_min_duration_ms":                            "log.min_duration_ms",
	"log_failed_queries":                             "log.failed_queries",
	"log_query_plan":                                 "log.query_plan",
}

// Normalize returns the canonical gflags spelling of a flag name: leading
// dashes dropped and hyphens turned into underscores. gflags looks a dashed
// name up again with its dashes replaced, so --log-level, -log_level and
// log_level all reach the flag declared as log_level; the operator spells
// every flag that way once so that two spellings of one flag cannot land as
// two entries.
func Normalize(flag string) string {
	return strings.ReplaceAll(strings.TrimLeft(flag, "-"), "-", "_")
}

// Setting returns the run-time setting name of a flag, in any spelling gflags
// accepts, and false for a flag Memgraph reads at startup only.
func Setting(flag string) (string, bool) {
	setting, ok := runtimeSettings[Normalize(flag)]
	return setting, ok
}

// IsRuntime reports whether a flag, in any spelling, can be changed on a
// running instance.
func IsRuntime(flag string) bool {
	_, ok := Setting(flag)
	return ok
}

// Change is one SET DATABASE SETTING to issue on one instance.
type Change struct {
	// Setting is the run-time setting's name, as SHOW DATABASE SETTINGS
	// reports it.
	Setting string
	// Value is the value the setting should hold: the flag's value, verbatim.
	// Memgraph stores every setting as a string and compares nothing, so the
	// flag value and the setting value are the same text.
	Value string
}

// Plan diffs the flags an instance should run with against the settings it
// reports and returns the changes that bring the run-time settings among them
// in line, ordered by setting name so a pass issues them the same way every
// time. Flags are keyed by any spelling gflags accepts; observed is the
// SHOW DATABASE SETTINGS view, setting name to value.
//
// Only flags the instance should run with are ever compared: a flag the
// table knows is planned when the instance reports a different value, or
// does not report the setting at all, which is how an image older than the
// table surfaces — as a SET the instance then rejects, which the controller
// reports. A flag missing from flags is left alone whatever the instance
// reports, so removing a flag changes nothing until the instance restarts
// without it. A startup-only flag is never planned.
func Plan(flags map[string]string, observed map[string]string) []Change {
	desired := make(map[string]string, len(flags))
	for flag, value := range flags {
		if setting, ok := Setting(flag); ok {
			desired[setting] = value
		}
	}
	return Diff(desired, observed)
}

// Diff is the diff behind Plan without the flag table: desired settings by
// their setting name against the observed view, the changes that bring the
// observed in line, ordered by name. It is what the cluster-wide coordinator
// settings use, since spec.coordinatorSettings already speaks in setting
// names. The rules are Plan's: only desired keys are compared, a setting the
// view does not report is planned, and a key absent from desired is left
// alone whatever the view says.
func Diff(desired map[string]string, observed map[string]string) []Change {
	var changes []Change
	for setting, value := range desired {
		if current, reported := observed[setting]; reported && current == value {
			continue
		}
		changes = append(changes, Change{Setting: setting, Value: value})
	}
	slices.SortFunc(changes, func(a, b Change) int {
		return strings.Compare(a.Setting, b.Setting)
	})
	return changes
}
