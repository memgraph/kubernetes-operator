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

package utils

import "fmt"

// The names a cluster member appears under in SHOW INSTANCES, derived from its
// StatefulSet pod ordinal. They are spelled out here rather than taken from the
// operator's own packages on purpose: a test that asked production code what it
// named an instance could never catch it renaming one.

// CoordinatorName is the SHOW INSTANCES name of the coordinator in the pod with
// the given ordinal: ordinal N registers as coordinator_N+1, because Raft
// coordinator IDs start at one.
func CoordinatorName(ordinal int32) string {
	return fmt.Sprintf("coordinator_%d", ordinal+1)
}

// CoordinatorOrdinal is the inverse of CoordinatorName: the ordinal of the pod
// running the coordinator a view names.
func CoordinatorOrdinal(name string) (int32, error) {
	var id int32
	if _, err := fmt.Sscanf(name, "coordinator_%d", &id); err != nil {
		return 0, fmt.Errorf("parsing coordinator name %q: %w", name, err)
	}
	return id - 1, nil
}

// DataInstanceName is the SHOW INSTANCES name of the data instance in the pod
// with the given ordinal: ordinal N registers as instance_N.
func DataInstanceName(ordinal int32) string {
	return fmt.Sprintf("instance_%d", ordinal)
}
