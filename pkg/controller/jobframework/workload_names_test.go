/*
Copyright The Kubernetes Authors.

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

package jobframework

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"

	"sigs.k8s.io/kueue/pkg/features"
)

func TestGenerateWorkloadNameWithExtra(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		ownerName    string
		ownerUID     types.UID
		ownerGVK     schema.GroupVersionKind
		extra        string
		wantPrefix   string
		want         string
	}{
		"empty extra": {
			ownerName:  "my-job",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			extra:      "",
			wantPrefix: "job-my-job-",
		},
		"generation as extra": {
			ownerName:  "my-job",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			extra:      "1",
			wantPrefix: "job-my-job-",
		},
		"resourceVersion as extra": {
			ownerName:  "my-job",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			extra:      "100",
			wantPrefix: "job-my-job-",
		},
		"custom extra string": {
			ownerName:  "my-job",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			extra:      "custom-part",
			wantPrefix: "job-my-job-",
		},
		"short name, no extra (ShortWorkloadNames=false)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			ownerName: "name",
			ownerUID:  "uid",
			ownerGVK:  gvk,
			extra:     "",
			want:      "job-name-ef3a1",
		},
		"short name, with extra (ShortWorkloadNames=false)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			ownerName: "name",
			ownerUID:  "uid",
			ownerGVK:  gvk,
			extra:     "1",
			want:      "job-name-2514c",
		},
		"long owner name truncated (ShortWorkloadNames=false)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			ownerName: "marketing-campaign-autumn-2024-region-north-america-department-digital-strategy-sub-department-social-media-engagement-tracker-and-analytics-dashboard-v2-final-revision-by-engineering-team-lead-senior-architect-validation-checked-approved-by-legal-and-ops-dept",
			ownerUID:  "uid",
			ownerGVK:  gvk,
			extra:     "",
			want:      "job-marketing-campaign-autumn-2024-region-north-america-department-digital-strategy-sub-department-social-media-engagement-tracker-and-analytics-dashboard-v2-final-revision-by-engineering-team-lead-senior-architect-validation-checked-approved-by-l-218b8",
		},
		"long owner name truncated (ShortWorkloadNames=true)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: true,
			},
			ownerName: "marketing-campaign-autumn-2024-region-north-america-department-digital-strategy-sub-department-social-media-engagement-tracker-and-analytics-dashboard-v2-final-revision-by-engineering-team-lead-senior-architect-validation-checked-approved-by-legal-and-ops-dept",
			ownerUID:  "uid",
			ownerGVK:  gvk,
			extra:     "",
			want:      "job-marketing-campaign-autumn-2024-region-north-america-d-218b8",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for fg, enabled := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enabled)
			}
			got := GenerateWorkloadNameWithExtra(tc.ownerName, tc.ownerUID, tc.ownerGVK, tc.extra)
			if tc.want != "" {
				if diff := cmp.Diff(tc.want, got); len(diff) != 0 {
					t.Fatalf("Unexpected workloadName (-want,+got):\n%s", diff)
				}
			}
			if tc.wantPrefix != "" {
				if !strings.HasPrefix(got, tc.wantPrefix) {
					t.Errorf("GenerateWorkloadNameWithExtra() = %q, want prefix %q", got, tc.wantPrefix)
				}
			}
			// Name should have prefix + "-" + 5-char hash
			prefix := GenerateWorkloadNamePrefix(tc.ownerName, tc.ownerUID, tc.ownerGVK)
			if len(got) != len(prefix)+1+hashLength {
				t.Errorf("GenerateWorkloadNameWithExtra() length = %d, want %d (prefix=%d + 1 + hash=%d)", len(got), len(prefix)+1+hashLength, len(prefix), hashLength)
			}
		})
	}

	// Verify name equality/inequality for different extra values.
	equalityCases := map[string]struct {
		extra1    string
		extra2    string
		wantEqual bool
	}{
		"different extra values produce different names": {
			extra1:    "1",
			extra2:    "2",
			wantEqual: false,
		},
		"different resourceVersion values produce different names": {
			extra1:    "100",
			extra2:    "200",
			wantEqual: false,
		},
		"same extra produces same name": {
			extra1:    "100",
			extra2:    "100",
			wantEqual: true,
		},
	}

	for name, tc := range equalityCases {
		t.Run(name, func(t *testing.T) {
			name1 := GenerateWorkloadNameWithExtra("my-job", "uid-123", gvk, tc.extra1)
			name2 := GenerateWorkloadNameWithExtra("my-job", "uid-123", gvk, tc.extra2)
			if tc.wantEqual {
				if diff := cmp.Diff(name1, name2); diff != "" {
					t.Errorf("expected same names (-want,+got):\n%s", diff)
				}
			} else {
				if name1 == name2 {
					t.Errorf("expected different names for different extra values, got %q for both", name1)
				}
			}
		})
	}

	t.Run("same prefix as GetWorkloadNameForOwnerWithGVK", func(t *testing.T) {
		elasticName := GenerateWorkloadNameWithExtra("my-job", "uid-123", gvk, "1")
		staticName := GetWorkloadNameForOwnerWithGVK("my-job", "uid-123", gvk)
		prefix := GenerateWorkloadNamePrefix("my-job", "uid-123", gvk)
		if !strings.HasPrefix(elasticName, prefix) {
			t.Errorf("elastic name %q does not have expected prefix %q", elasticName, prefix)
		}
		if !strings.HasPrefix(staticName, prefix) {
			t.Errorf("static name %q does not have expected prefix %q", staticName, prefix)
		}
		// The hash suffix should differ since elastic uses extra.
		if elasticName == staticName {
			t.Errorf("elastic and static names should differ, both are %q", elasticName)
		}
	})
}

func TestGenerateWorkloadNameWithGeneration(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

	cases := map[string]struct {
		ownerName1  string
		uid1        types.UID
		generation1 int64
		ownerName2  string
		uid2        types.UID
		generation2 int64
		wantEqual   bool
	}{
		"different generations produce different names": {
			ownerName1:  "my-job",
			uid1:        "uid-123",
			generation1: 1,
			ownerName2:  "my-job",
			uid2:        "uid-123",
			generation2: 2,
			wantEqual:   false,
		},
		"same generation produces same name": {
			ownerName1:  "my-job",
			uid1:        "uid-123",
			generation1: 1,
			ownerName2:  "my-job",
			uid2:        "uid-123",
			generation2: 1,
			wantEqual:   true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			name1 := GetWorkloadNameForOwnerWithGVKAndGeneration(tc.ownerName1, tc.uid1, gvk, tc.generation1)
			name2 := GetWorkloadNameForOwnerWithGVKAndGeneration(tc.ownerName2, tc.uid2, gvk, tc.generation2)
			if tc.wantEqual {
				if diff := cmp.Diff(name1, name2); diff != "" {
					t.Errorf("expected same names (-want,+got):\n%s", diff)
				}
			} else {
				if name1 == name2 {
					t.Errorf("expected different names, got %q for both", name1)
				}
			}
		})
	}

	t.Run("consistent with GenerateWorkloadNameWithExtra using string generation", func(t *testing.T) {
		nameFromGeneration := GetWorkloadNameForOwnerWithGVKAndGeneration("my-job", "uid-123", gvk, 42)
		nameFromExtra := GenerateWorkloadNameWithExtra("my-job", "uid-123", gvk, "42")
		if diff := cmp.Diff(nameFromGeneration, nameFromExtra); diff != "" {
			t.Errorf("GenerateWorkloadNameWithGeneration and GenerateWorkloadNameWithExtra should produce same result (-want,+got):\n%s", diff)
		}
	})
}

func TestGetWorkloadNameForVariant(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		parentName   string
		ownerUID     types.UID
		ownerGVK     schema.GroupVersionKind
		flavor       string
		want         string
	}{
		"simple flavor": {
			parentName: "job-my-job-12345",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			flavor:     "spot",
			want:       "job-my-job-variant-spot-6320b",
		},
		"flavor name exceeding max length (ShortWorkloadNames=false)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			parentName: "job-my-job-12345",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			flavor:     strings.Repeat("a", 300),
			want:       "job-my-job-variant-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-2e907",
		},
		"flavor name exceeding max length (ShortWorkloadNames=true)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: true,
			},
			parentName: "job-my-job-12345",
			ownerUID:   "uid-123",
			ownerGVK:   gvk,
			flavor:     strings.Repeat("a", 300),
			want:       "job-my-job-variant-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-2e907",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for fg, enabled := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enabled)
			}
			got := GetWorkloadNameForVariant(tc.parentName, tc.ownerUID, tc.ownerGVK, tc.flavor)
			if tc.want != "" {
				if diff := cmp.Diff(tc.want, got); len(diff) != 0 {
					t.Fatalf("Unexpected workloadName (-want,+got):\n%s", diff)
				}
			}
			parentPrefix := tc.parentName[:strings.LastIndex(tc.parentName, "-")]
			prefixWithFlavor := truncate(fmt.Sprintf("%s-variant-%s", parentPrefix, tc.flavor), maxPrefixLength())
			wantLength := len(prefixWithFlavor) + 1 + hashLength
			if len(got) != wantLength {
				t.Errorf("GetWorkloadNameForVariant() length = %d, want %d", len(got), wantLength)
			}
		})
	}

	equalityCases := map[string]struct {
		flavor1   string
		flavor2   string
		wantEqual bool
	}{
		"different flavors produce different names": {
			flavor1:   "flavor1",
			flavor2:   "flavor2",
			wantEqual: false,
		},
		"same flavor produces same name": {
			flavor1:   "flavor-x",
			flavor2:   "flavor-x",
			wantEqual: true,
		},
	}

	for name, tc := range equalityCases {
		t.Run(name, func(t *testing.T) {
			name1 := GetWorkloadNameForVariant("job-my-job-12345", "uid-123", gvk, tc.flavor1)
			name2 := GetWorkloadNameForVariant("job-my-job-12345", "uid-123", gvk, tc.flavor2)
			if tc.wantEqual {
				if diff := cmp.Diff(name1, name2); diff != "" {
					t.Errorf("expected same names (-want,+got):\n%s", diff)
				}
			} else {
				if name1 == name2 {
					t.Errorf("expected different names for different flavors, got %q for both", name1)
				}
			}
		})
	}

	t.Run("parent name exceeding max length with different flavors have same prefix but different hashes", func(t *testing.T) {
		parentName := "job-" + strings.Repeat("a", 300) + "-12345"
		uid := types.UID("uid-123")

		name1 := GetWorkloadNameForVariant(parentName, uid, gvk, "flavor1")
		name2 := GetWorkloadNameForVariant(parentName, uid, gvk, "flavor2")

		prefix1 := name1[:len(name1)-hashLength-1]
		prefix2 := name2[:len(name2)-hashLength-1]

		if prefix1 != prefix2 {
			t.Errorf("prefixes should be identical, got %q and %q", prefix1, prefix2)
		}

		if name1 == name2 {
			t.Errorf("total names should be different because hashes differ, got %q for both", name1)
		}
	})
}
