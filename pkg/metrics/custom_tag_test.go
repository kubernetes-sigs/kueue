package metrics

import (
	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	configv1beta1 "sigs.k8s.io/kueue/apis/config/v1beta1"
	"testing"
)

func TestGetPrometheusTag(t *testing.T) {
	overrideTag := "custom_tag"
	tests := []struct {
		name string
		tag  configv1beta1.CustomMetricTag
		want string
	}{
		{
			name: "no override, use resource tag",
			tag: configv1beta1.CustomMetricTag{
				ResourceTag: "resource_label",
			},
			want: "resource_label",
		},
		{
			name: "with override, use override tag",
			tag: configv1beta1.CustomMetricTag{
				ResourceTag:       "resource_label",
				OverrideMetricTag: &overrideTag,
			},
			want: "custom_tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPrometheusTag(tt.tag)
			if got != tt.want {
				t.Errorf("getPrometheusTag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetConfiguration(t *testing.T) {
	overrideTag1 := "custom_cq_tag"
	overrideTag2 := "custom_lq_tag"

	tests := []struct {
		name             string
		customMetricTags configv1beta1.CustomMetricTags
		want             CustomTagsConfiguration
	}{
		{
			name: "cluster queue tags only",
			customMetricTags: configv1beta1.CustomMetricTags{
				ClusterQueue: []configv1beta1.CustomMetricTag{
					{ResourceTag: "team"},
					{ResourceTag: "env"},
				},
				LocalQueue: []configv1beta1.CustomMetricTag{},
			},
			want: CustomTagsConfiguration{
				ClusterQueue: CustomTagsObjectConfiguration{
					ResourceTags: []string{"team", "env"},
					MetricTags:   []string{"team", "env"},
				},
				LocalQueue: CustomTagsObjectConfiguration{
					ResourceTags: []string{},
					MetricTags:   []string{},
				},
			},
		},
		{
			name: "local queue tags only",
			customMetricTags: configv1beta1.CustomMetricTags{
				ClusterQueue: []configv1beta1.CustomMetricTag{},
				LocalQueue: []configv1beta1.CustomMetricTag{
					{ResourceTag: "project"},
				},
			},
			want: CustomTagsConfiguration{
				ClusterQueue: CustomTagsObjectConfiguration{
					ResourceTags: []string{},
					MetricTags:   []string{},
				},
				LocalQueue: CustomTagsObjectConfiguration{
					ResourceTags: []string{"project"},
					MetricTags:   []string{"project"},
				},
			},
		},
		{
			name: "both cluster and local queue tags with overrides",
			customMetricTags: configv1beta1.CustomMetricTags{
				ClusterQueue: []configv1beta1.CustomMetricTag{
					{ResourceTag: "team", OverrideMetricTag: &overrideTag1},
					{ResourceTag: "env"},
				},
				LocalQueue: []configv1beta1.CustomMetricTag{
					{ResourceTag: "project", OverrideMetricTag: &overrideTag2},
				},
			},
			want: CustomTagsConfiguration{
				ClusterQueue: CustomTagsObjectConfiguration{
					ResourceTags: []string{"team", "env"},
					MetricTags:   []string{"custom_cq_tag", "env"},
				},
				LocalQueue: CustomTagsObjectConfiguration{
					ResourceTags: []string{"project"},
					MetricTags:   []string{"custom_lq_tag"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getConfiguration(&tt.customMetricTags)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("getConfiguration() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetResourceTagValues(t *testing.T) {
	tests := []struct {
		name       string
		obj        metav1.Object
		customTags CustomTagsObjectConfiguration
		want       []string
	}{
		{
			name: "empty tags configuration",
			obj: &metav1.ObjectMeta{
				Name: "test-queue",
				Labels: map[string]string{
					"team": "platform",
				},
			},
			customTags: CustomTagsObjectConfiguration{
				ResourceTags: []string{},
			},
			want: []string{},
		},
		{
			name: "tags from labels only",
			obj: &metav1.ObjectMeta{
				Name: "test-queue",
				Labels: map[string]string{
					"team": "platform",
					"env":  "production",
				},
			},
			customTags: CustomTagsObjectConfiguration{
				ResourceTags: []string{"team", "env"},
			},
			want: []string{"platform", "production"},
		},
		{
			name: "tags from annotations only",
			obj: &metav1.ObjectMeta{
				Name: "test-queue",
				Annotations: map[string]string{
					"team": "data",
					"cost": "high",
				},
			},
			customTags: CustomTagsObjectConfiguration{
				ResourceTags: []string{"team", "cost"},
			},
			want: []string{"data", "high"},
		},
		{
			name: "labels take precedence over annotations",
			obj: &metav1.ObjectMeta{
				Name: "test-queue",
				Labels: map[string]string{
					"team": "platform",
				},
				Annotations: map[string]string{
					"team": "data",
				},
			},
			customTags: CustomTagsObjectConfiguration{
				ResourceTags: []string{"team"},
			},
			want: []string{"platform"},
		},
		{
			name: "missing tags return empty strings",
			obj: &metav1.ObjectMeta{
				Name: "test-queue",
				Labels: map[string]string{
					"team": "platform",
				},
			},
			customTags: CustomTagsObjectConfiguration{
				ResourceTags: []string{"team", "missing_tag"},
			},
			want: []string{"platform", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getResourceTagValues(tt.obj, tt.customTags)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("getResourceTagValues() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
