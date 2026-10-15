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
}

// Eventf renders an Event as "<type> <reason> <note>", plus " [<name>]" naming
// the related object when there is one.
func (e *recordedEvents) Eventf(
	_ runtime.Object, related runtime.Object, eventtype, reason, _, note string, args ...any,
) {
	line := eventtype + " " + reason + " " + fmt.Sprintf(note, args...)
	if related != nil {
		if accessor, err := meta.Accessor(related); err == nil {
			line += " [" + accessor.GetName() + "]"
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, line)
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
