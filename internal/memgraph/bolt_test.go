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

// TestDialSchemes pins the connector's contract for a cluster mid-roll: the
// mode the spec asks for is dialed first, and the other is the fallback that
// reaches a coordinator not yet restarted into the new mode. Both orders must
// hold, because a roll runs in both directions.
func TestDialSchemes(t *testing.T) {
	tests := []struct {
		name string
		tls  bool
		want []string
	}{
		{name: "TLS asked for, plaintext as fallback", tls: true, want: []string{"bolt+ssc", "bolt"}},
		{name: "plaintext asked for, TLS as fallback", tls: false, want: []string{"bolt", "bolt+ssc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, dialSchemes(tt.tls)); diff != "" {
				t.Errorf("dialSchemes(%t) mismatch (-want +got):\n%s", tt.tls, diff)
			}
		})
	}
}

// TestShowSettingsParsing pins the SHOW DATABASE SETTINGS column names the
// client reads a setting off the wire by, and that a row without a name is
// refused rather than filed under the empty string.
func TestShowSettingsParsing(t *testing.T) {
	const level = "DEBUG"
	columns := []string{"setting_name", "setting_value"}
	records := []*db.Record{
		{Keys: columns, Values: []any{logLevelSetting, level}},
		{Keys: columns, Values: []any{"storage.snapshot.interval", ""}},
	}
	got, err := settingsFromRecords(records)
	if err != nil {
		t.Fatalf("settingsFromRecords() error = %v", err)
	}
	want := map[string]string{logLevelSetting: level, "storage.snapshot.interval": ""}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("settingsFromRecords() mismatch (-want +got):\n%s", diff)
	}

	if _, err := settingsFromRecords([]*db.Record{
		{Keys: columns[1:], Values: []any{level}},
	}); err == nil {
		t.Error("settingsFromRecords() accepted a row without a setting_name")
	}
}
