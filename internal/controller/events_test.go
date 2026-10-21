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
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
)

// recordedEvents is the Event recorder the suite hands the reconciler: it keeps
// every Event in order instead of sending it, so a spec can assert what a pass
// told the user it did. client-go's FakeRecorder blocks once its channel is
// full, which a spec running a dozen passes would reach.
type recordedEvents struct {
	mu     sync.Mutex
	events []string
	// keys holds each Event's isomorphism key, parallel to events.
	keys []string
}

// Eventf renders an Event as "<type> <reason> <note>", plus " [<name>]" naming
// the related object when there is one.
func (e *recordedEvents) Eventf(
	_ runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...any,
) {
	line := eventtype + " " + reason + " " + fmt.Sprintf(note, args...)
	// What client-go's events broadcaster compares to fold an Event into an
	// earlier one (getKey in tools/events): everything but the note, with the
	// regarding object always the cluster here.
	key := eventtype + "/" + reason + "/" + action
	if related != nil {
		if accessor, err := meta.Accessor(related); err == nil {
			line += " [" + accessor.GetName() + "]"
			key += "/" + fmt.Sprintf("%T", related) + "/" + accessor.GetName()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, line)
	e.keys = append(e.keys, key)
}

// merged returns the Events from index since on that client-go would fold
// into an earlier one of them instead of showing: same type, reason, action
// and related object, whatever the note says.
func (e *recordedEvents) merged(since int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var merged []string
	for i := since; i < len(e.events); i++ {
		if seen[e.keys[i]] {
			merged = append(merged, e.events[i])
		}
		seen[e.keys[i]] = true
	}
	return merged
}

// all returns every Event recorded so far.
func (e *recordedEvents) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

// withReason returns the Events recorded with the given reason, in order.
func (e *recordedEvents) withReason(reason string) []string {
	var matched []string
	for _, event := range e.all() {
		if strings.SplitN(event, " ", 3)[1] == reason {
			matched = append(matched, event)
		}
	}
	return matched
}
