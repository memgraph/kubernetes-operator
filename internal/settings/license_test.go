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

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLicense(t *testing.T) {
	const (
		oldLicense = "old-license"
		newLicense = "new-license"
		org        = "Memgraph"
		otherOrg   = "Other"
	)
	for _, tc := range []struct {
		name     string
		observed map[string]string
		want     []Change
	}{
		{
			name:     "an instance already on the Secret's license is left alone",
			observed: map[string]string{LicenseSetting: newLicense, OrganizationSetting: org},
		},
		{
			name:     "a renewal for the same organization is one SET of the license",
			observed: map[string]string{LicenseSetting: oldLicense, OrganizationSetting: org},
			want:     []Change{{Setting: LicenseSetting, Value: newLicense}},
		},
		{
			name:     "a different organization and license are set, the organization first",
			observed: map[string]string{LicenseSetting: oldLicense, OrganizationSetting: otherOrg},
			want: []Change{
				{Setting: OrganizationSetting, Value: org},
				{Setting: LicenseSetting, Value: newLicense},
			},
		},
		{
			name:     "a different organization alone is one SET of the organization",
			observed: map[string]string{LicenseSetting: newLicense, OrganizationSetting: otherOrg},
			want:     []Change{{Setting: OrganizationSetting, Value: org}},
		},
		{
			name:     "an instance that never held a valid license gets both",
			observed: map[string]string{LicenseSetting: "", OrganizationSetting: ""},
			want: []Change{
				{Setting: OrganizationSetting, Value: org},
				{Setting: LicenseSetting, Value: newLicense},
			},
		},
		{
			name:     "an instance reporting neither setting gets both",
			observed: map[string]string{},
			want: []Change{
				{Setting: OrganizationSetting, Value: org},
				{Setting: LicenseSetting, Value: newLicense},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, License(newLicense, org, tc.observed)); diff != "" {
				t.Errorf("License() changes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
