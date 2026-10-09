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

// Package expectations remembers the StatefulSet writes the operator made and
// its informer cache has not shown back yet. A pass reads its StatefulSets
// from the cache, which can lag a write the previous pass made; for most
// decisions that is harmless, because the apply restates the same desired
// state, but a shrink is the one write whose stale read undoes it: the replica
// count a pass applies is the larger of the declared one and the one the
// StatefulSet carries, so a cache still showing the pre-shrink count would
// apply it again and recreate the pods the shrink just shed.
//
// The pattern is the Kubernetes controllers' expectations, in the shape ECK
// uses for StatefulSets: the write's UID and generation are recorded, and a
// cached copy is trusted again once it is at least that generation, or is a
// different object, or is gone. Nothing is persisted. A restarted operator
// fills its cache from a fresh list that already holds every write it made,
// so only the process that made a write can be ahead of its own cache, and
// that process is the one holding the expectation.
package expectations

import (
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
)

// StatefulSets holds the outstanding expectation of each StatefulSet the
// operator wrote. Its zero value is ready to use, and it is safe for
// concurrent use.
//
// An expectation for a StatefulSet whose cluster is deleted before the cache
// catches up is never cleared, which leaves a few bytes behind and nothing
// else: a StatefulSet created under the same name later has a different UID,
// which satisfies it.
type StatefulSets struct {
	mu       sync.Mutex
	expected map[types.NamespacedName]written
}

// written is the StatefulSet a write left behind, as the API server returned
// it.
type written struct {
	uid        types.UID
	generation int64
}

// Expect records a write to the named StatefulSet that the API server answered
// with the given UID and generation. A later write replaces an earlier one, so
// only the newest is waited for.
func (s *StatefulSets) Expect(name types.NamespacedName, uid types.UID, generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expected == nil {
		s.expected = map[types.NamespacedName]written{}
	}
	s.expected[name] = written{uid: uid, generation: generation}
}

// Satisfied reports whether cached, the named StatefulSet as the cache shows
// it (nil when the cache has none), can be trusted: it is no older than the
// last write expected of it. A satisfied expectation is forgotten.
func (s *StatefulSets) Satisfied(name types.NamespacedName, cached *appsv1.StatefulSet) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want, ok := s.expected[name]
	if !ok {
		return true
	}
	// Gone, or deleted and created again under the same name: either way the
	// object written is not the one a pass would act on any more.
	if cached == nil || cached.UID != want.uid || cached.Generation >= want.generation {
		delete(s.expected, name)
		return true
	}
	return false
}
