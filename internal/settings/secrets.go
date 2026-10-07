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

// The run-time settings Memgraph keeps its AWS configuration in, as SHOW
// DATABASE SETTINGS names them. The awsCredentials Secret is applied under
// these names with Diff, like any setting the spec names directly.
const (
	AWSAccessKeySetting   = "aws.access_key"
	AWSSecretKeySetting   = "aws.secret_key"
	AWSRegionSetting      = "aws.region"
	AWSEndpointURLSetting = "aws.endpoint_url"
)

// License diffs the license and organization name an instance should run with
// against the settings it reports, and returns the changes that bring it in
// line: the organization first, then the license, each only when it differs.
func License(license, organization string, observed map[string]string) []Change {
	var changes []Change
	if observed[OrganizationSetting] != organization {
		changes = append(changes, Change{Setting: OrganizationSetting, Value: organization})
	}
	if observed[LicenseSetting] != license {
		changes = append(changes, Change{Setting: LicenseSetting, Value: license})
	}
	return changes
}
