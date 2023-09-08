package config

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestValidate(t *testing.T) {
	testcases := []struct {
		name    string
		cfg     *configapi.Configuration
		wantErr field.ErrorList
	}{

		{
			name:    "empty",
			cfg:     &configapi.Configuration{},
			wantErr: nil,
		},
		{
			name: "invalid queue visibility UpdateIntervalSeconds",
			cfg: &configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					UpdateIntervalSeconds: 0,
				},
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("queueVisibility").Child("updateIntervalSeconds"), 0, fmt.Sprintf("greater than or equal to %d", queueVisibilityClusterQueuesUpdateIntervalSeconds)),
			},
		},
		{
			name: "invalid queue visibility cluster queue max count",
			cfg: &configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					ClusterQueues: &configapi.ClusterQueueVisibility{
						MaxCount: 4001,
					},
					UpdateIntervalSeconds: 1,
				},
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("queueVisibility").Child("clusterQueues").Child("maxCount"), 4001, fmt.Sprintf("must be less than %d", queueVisibilityClusterQueuesMaxValue)),
			},
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantErr, validate(tc.cfg), cmpopts.IgnoreFields(field.Error{}, "BadValue")); diff != "" {
				t.Errorf("Unexpected returned error (-want,+got):\n%s", diff)
			}
		})
	}
}
