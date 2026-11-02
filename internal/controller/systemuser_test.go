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
	"strings"
	"testing"
)

// memgraphPBKDF2Literal is Memgraph's own known-answer vector, the password
// "hello" with salt 00..0f at 600000 iterations, in the pre-hashed form
// Memgraph reads (its unit tests carry the same derived key). Matching it is
// what proves a hash the operator writes is one Memgraph accepts. Reproduce:
//
//	python3 -c 'import hashlib; print(hashlib.pbkdf2_hmac(
//	    "sha256", b"hello", bytes(range(16)), 600000, 32).hex())'
const memgraphPBKDF2Literal = "pbkdf2-sha256:600000:000102030405060708090a0b0c0d0e0f:" +
	"0f2f8ea738901d7afee33ef32d3a4358619f9f6e24a1f1b6a6ca81310858fcd7"

func TestSystemUserPBKDF2MatchesMemgraph(t *testing.T) {
	if !systemUserHashMatches(memgraphPBKDF2Literal, "hello", true) {
		t.Fatal("the operator does not derive Memgraph's known-answer pbkdf2-sha256 hash")
	}
	if systemUserHashMatches(memgraphPBKDF2Literal, "hellp", true) {
		t.Error("a wrong password matched")
	}
	// Under the other algorithm the same literal is not the hash the pass
	// wants, so it is rewritten rather than kept.
	if systemUserHashMatches(memgraphPBKDF2Literal, "hello", false) {
		t.Error("a pbkdf2-sha256 hash matched where a bcrypt one is wanted")
	}
}

// TestSystemUserPBKDF2RejectsWhatMemgraphReadsDifferently pins that a hash
// Memgraph would not read as the operator's own counts as stale, so the pass
// rewrites it instead of keeping a file every FIPS pod refuses.
func TestSystemUserPBKDF2RejectsWhatMemgraphReadsDifferently(t *testing.T) {
	salt, key, _ := strings.Cut(strings.TrimPrefix(memgraphPBKDF2Literal, "pbkdf2-sha256:600000:"), ":")
	for name, hash := range map[string]string{
		"another iteration count": "pbkdf2-sha256:1000:" + salt + ":" + key,
		"uppercase hex":           "pbkdf2-sha256:600000:" + salt + ":" + strings.ToUpper(key),
		"no separator":            "pbkdf2-sha256:600000:" + salt + key,
		"a short salt":            "pbkdf2-sha256:600000:" + salt[2:] + ":" + key,
		"a short key":             "pbkdf2-sha256:600000:" + salt + ":" + key[2:],
		"surrounding whitespace":  memgraphPBKDF2Literal + "\n",
		"bcrypt":                  "bcrypt:$2a$10$abcdefghijklmnopqrstuuMEoOS8kpMBFQZfQ6Ywa/7ZMlYOT.Oi2",
	} {
		if systemUserHashMatches(hash, "hello", true) {
			t.Errorf("%s: matched", name)
		}
	}
}

func TestSystemUserHashFollowsTheAlgorithm(t *testing.T) {
	for _, fips := range []bool{false, true} {
		hash, err := hashSystemUserPassword("s3cret", fips)
		if err != nil {
			t.Fatalf("fips=%v: %v", fips, err)
		}
		wantPrefix := map[bool]string{false: "bcrypt:", true: "pbkdf2-sha256:600000:"}[fips]
		if !strings.HasPrefix(hash, wantPrefix) {
			t.Errorf("fips=%v: hash %q does not start with %q", fips, hash, wantPrefix)
		}
		if !systemUserHashMatches(hash, "s3cret", fips) {
			t.Errorf("fips=%v: the hash does not match its own password", fips)
		}
		if systemUserHashMatches(hash, "s3cret", !fips) {
			t.Errorf("fips=%v: the hash matched under the other algorithm", fips)
		}
	}
}
