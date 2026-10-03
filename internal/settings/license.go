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

package settings

// The run-time settings Memgraph keeps the enterprise license in, as SHOW
// DATABASE SETTINGS names them. They are not flags: the pod reads the license
// from its environment at startup, and Memgraph stores whichever valid license
// it ends up with under these two names, which is also where a SET DATABASE
// SETTING changes it on a running instance.
const (
	LicenseSetting      = "enterprise.license"
	OrganizationSetting = "organization.name"
)

// License diffs the license and organization name an instance should run with
// against the settings it reports, and returns the changes that bring it in
// line: the organization first, then the license, each only when it differs.
//
// Memgraph checks the pair after each SET and keeps whichever valid pair
// expires last, writing the winner back to these settings. A pod still holds
// the pair it started with in its environment, so while that pair is valid a
// SET of either half alone produces a pair that does not match, the old one
// wins, and the SET is undone. A renewal for the same organization is a single
// SET and lands; a different organization cannot be moved on a running
// instance at all. organizationChanged reports that case, and no change is
// planned for it: the pod picks the new pair up from its environment when it
// restarts. An instance reporting no organization has never held a valid
// license, so there is no pair to lose to and both changes are planned.
func License(license, organization string, observed map[string]string) (changes []Change, organizationChanged bool) {
	current := observed[OrganizationSetting]
	if current != organization {
		if current != "" {
			return nil, true
		}
		changes = append(changes, Change{Setting: OrganizationSetting, Value: organization})
	}
	if observed[LicenseSetting] != license {
		changes = append(changes, Change{Setting: LicenseSetting, Value: license})
	}
	return changes, false
}
