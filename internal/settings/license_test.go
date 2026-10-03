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
		name                    string
		observed                map[string]string
		want                    []Change
		wantOrganizationChanged bool
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
			name:                    "a different organization is reported and nothing is set",
			observed:                map[string]string{LicenseSetting: oldLicense, OrganizationSetting: otherOrg},
			wantOrganizationChanged: true,
		},
		{
			name:                    "a different organization is reported even when the license already matches",
			observed:                map[string]string{LicenseSetting: newLicense, OrganizationSetting: otherOrg},
			wantOrganizationChanged: true,
		},
		{
			name:     "an instance that never held a valid license gets the organization, then the license",
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
			got, organizationChanged := License(newLicense, org, tc.observed)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("License() changes mismatch (-want +got):\n%s", diff)
			}
			if organizationChanged != tc.wantOrganizationChanged {
				t.Errorf("License() organizationChanged = %v, want %v", organizationChanged, tc.wantOrganizationChanged)
			}
		})
	}
}
