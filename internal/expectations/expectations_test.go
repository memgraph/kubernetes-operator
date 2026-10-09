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

package expectations

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var name = types.NamespacedName{Namespace: "default", Name: "cluster-data"}

func statefulSet(uid types.UID, generation int64) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: name.Namespace, Name: name.Name, UID: uid, Generation: generation,
	}}
}

func TestSatisfied(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		cached *appsv1.StatefulSet
		want   bool
	}{
		{"an older generation of the object written", statefulSet("a", 4), false},
		{"the generation written", statefulSet("a", 5), true},
		{"a newer generation, written since by someone else", statefulSet("a", 6), true},
		{"no StatefulSet at all", nil, true},
		{"another object under the same name", statefulSet("b", 1), true},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			var s StatefulSets
			s.Expect(name, "a", 5)
			if got := s.Satisfied(name, tc.cached); got != tc.want {
				t.Fatalf("Satisfied() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNothingExpected(t *testing.T) {
	var s StatefulSets
	if !s.Satisfied(name, statefulSet("a", 1)) {
		t.Fatal("a StatefulSet nothing was written to must be trusted")
	}
}

// A satisfied expectation is forgotten, so a cache that later shows an older
// copy again — which an informer never does, but a test double can — is
// trusted rather than waited on forever.
func TestSatisfiedIsForgotten(t *testing.T) {
	var s StatefulSets
	s.Expect(name, "a", 5)
	if !s.Satisfied(name, statefulSet("a", 5)) {
		t.Fatal("the generation written must satisfy the expectation")
	}
	if !s.Satisfied(name, statefulSet("a", 4)) {
		t.Fatal("a satisfied expectation must be forgotten")
	}
}

// Only the newest write is waited for: a second shrink before the cache shows
// the first raises the bar to the second.
func TestLaterWriteReplacesEarlier(t *testing.T) {
	var s StatefulSets
	s.Expect(name, "a", 5)
	s.Expect(name, "a", 6)
	if s.Satisfied(name, statefulSet("a", 5)) {
		t.Fatal("the first write's generation must not satisfy the second")
	}
	if !s.Satisfied(name, statefulSet("a", 6)) {
		t.Fatal("the second write's generation must satisfy it")
	}
}
