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

import "fmt"

// The HA management query grammar, mirroring what the memgraph-high-availability
// Helm chart's registration job issues. Config values are rendered inline
// because Memgraph's coordinator queries do not accept Bolt parameters; all
// inputs are operator-derived names and "host:port" addresses, never user text.

const showInstancesQuery = "SHOW INSTANCES"

func addCoordinatorQuery(coordinator CoordinatorSpec) string {
	return fmt.Sprintf(
		`ADD COORDINATOR %d WITH CONFIG {"bolt_server": %q, "coordinator_server": %q, "management_server": %q}`,
		coordinator.ID,
		coordinator.BoltServer,
		coordinator.CoordinatorServer,
		coordinator.ManagementServer,
	)
}

func registerInstanceQuery(instance DataInstanceSpec) string {
	return fmt.Sprintf(
		`REGISTER INSTANCE %s WITH CONFIG {"bolt_server": %q, "management_server": %q, "replication_server": %q}`,
		instance.Name,
		instance.BoltServer,
		instance.ManagementServer,
		instance.ReplicationServer,
	)
}

func setInstanceToMainQuery(name string) string {
	return fmt.Sprintf("SET INSTANCE %s TO MAIN", name)
}
