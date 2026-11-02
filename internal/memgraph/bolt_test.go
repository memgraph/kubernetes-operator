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
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
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

// TestDialCredentials pins the order the connector logs in with: the system
// user when the pass carries its password, then no credentials, which is what
// still reaches an instance that has no system user (Memgraph before 3.15, or
// a pod not yet restarted to mount the hash). Without a password the system
// user is never tried.
func TestDialCredentials(t *testing.T) {
	const password = "s3cret"
	asTokens := func(creds []credentials) []map[string]any {
		tokens := make([]map[string]any, 0, len(creds))
		for _, c := range creds {
			tokens = append(tokens, c.token.Tokens)
		}
		return tokens
	}
	none := neo4j.NoAuth().Tokens

	if diff := cmp.Diff([]map[string]any{none}, asTokens(dialCredentials(context.Background()))); diff != "" {
		t.Errorf("dialCredentials() without a password mismatch (-want +got):\n%s", diff)
	}

	ctx := WithSystemUserPassword(context.Background(), password)
	if got := SystemUserPassword(ctx); got != password {
		t.Errorf("SystemUserPassword() = %q, want the password the context was given", got)
	}
	want := []map[string]any{neo4j.BasicAuth(SystemUserName, password, "").Tokens, none}
	if diff := cmp.Diff(want, asTokens(dialCredentials(ctx))); diff != "" {
		t.Errorf("dialCredentials() with a password mismatch (-want +got):\n%s", diff)
	}
}

// TestIsAuthenticationFailure pins what moves the connector on to the next
// credentials: Memgraph refusing them, however the driver wraps the error.
// Anything else — the connection failing, the session limit — is not worth
// another login and moves on to the next scheme instead.
func TestIsAuthenticationFailure(t *testing.T) {
	refused := &neo4j.Neo4jError{Code: "Memgraph.ClientError.Security.Unauthenticated", Msg: "Authentication failure"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "credentials refused", err: refused, want: true},
		{name: "credentials refused, wrapped", err: fmt.Errorf("verifying connectivity: %w", refused), want: true},
		{
			name: "session limit",
			err:  &neo4j.Neo4jError{Code: "Memgraph.ClientError.Statement.SessionLimitReached"},
			want: false,
		},
		{name: "connection failed", err: errors.New("dial tcp: connection refused"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAuthenticationFailure(tt.err); got != tt.want {
				t.Errorf("isAuthenticationFailure(%v) = %t, want %t", tt.err, got, tt.want)
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

// TestShowConfigParsing pins the SHOW CONFIG columns the client reads a flag
// by: the name and its current value, the two of its four columns the
// operator uses.
func TestShowConfigParsing(t *testing.T) {
	const nameColumn, level = "name", "DEBUG"
	columns := []string{nameColumn, "default_value", "current_value", "description"}
	records := []*db.Record{
		{Keys: columns, Values: []any{"log_level", "WARNING", level, "Minimum log level."}},
		{Keys: columns, Values: []any{"memory_limit", "0", "0", "Total memory limit in MiB."}},
	}
	got, err := namedValuesFromRecords(records, nameColumn, "current_value")
	if err != nil {
		t.Fatalf("namedValuesFromRecords() error = %v", err)
	}
	want := map[string]string{"log_level": level, "memory_limit": "0"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SHOW CONFIG parsing mismatch (-want +got):\n%s", diff)
	}
}

// TestConnectGivesUpOnSilentServer pins that a server which accepts the
// connection and never answers costs one call timeout per scheme rather than
// blocking forever: the driver itself bounds only the TCP connect.
func TestConnectGivesUpOnSilentServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Held open and never written to, until the test ends.
			defer func() { _ = conn.Close() }()
		}
	}()

	const timeout = 200 * time.Millisecond
	start := time.Now()
	_, err = boltConnector{timeout: timeout}.Connect(context.Background(), listener.Addr().String(), false)
	if err == nil {
		t.Fatal("Connect() succeeded against a server that never answered")
	}
	if elapsed := time.Since(start); elapsed > 10*timeout {
		t.Errorf("Connect() took %v, want about %v per scheme", elapsed, timeout)
	}
}
