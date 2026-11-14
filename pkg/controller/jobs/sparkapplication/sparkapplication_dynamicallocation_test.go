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

package sparkapplication

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
)

func TestDynamicAllocation(t *testing.T) {
	type want struct {
		Enabled                                      bool
		MinExecutors, MaxExecutors, InitialExecutors *int32
		ExecutorInstances                            *int32
	}
	cases := map[string]struct {
		typed     *sparkv1beta2.DynamicAllocation
		instances *int32
		sparkConf map[string]string
		want      want
	}{
		"nothing set": {},
		"typed only": {
			typed: &sparkv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), MaxExecutors: new(int32(4)), InitialExecutors: new(int32(2))},
			want:  want{Enabled: true, MinExecutors: new(int32(1)), MaxExecutors: new(int32(4)), InitialExecutors: new(int32(2))},
		},
		"sparkConf only": {
			sparkConf: map[string]string{
				"spark.dynamicAllocation.enabled":          "true",
				"spark.dynamicAllocation.minExecutors":     "2",
				"spark.dynamicAllocation.maxExecutors":     "6",
				"spark.dynamicAllocation.initialExecutors": "3",
				"spark.executor.instances":                 "1",
			},
			want: want{Enabled: true, MinExecutors: new(int32(2)), MaxExecutors: new(int32(6)), InitialExecutors: new(int32(3)), ExecutorInstances: new(int32(1))},
		},
		"typed fields win over sparkConf, unset typed fields fall back to it": {
			typed: &sparkv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(8))},
			sparkConf: map[string]string{
				"spark.dynamicAllocation.minExecutors": "2",
				"spark.dynamicAllocation.maxExecutors": "6",
			},
			want: want{Enabled: true, MinExecutors: new(int32(2)), MaxExecutors: new(int32(8))},
		},
		// The operator emits no typed field when spec.dynamicAllocation.enabled is false,
		// so Spark runs with what spec.sparkConf says.
		"typed disabled does not override sparkConf": {
			typed: &sparkv1beta2.DynamicAllocation{Enabled: false, MaxExecutors: new(int32(8))},
			sparkConf: map[string]string{
				"spark.dynamicAllocation.enabled":      "true",
				"spark.dynamicAllocation.maxExecutors": "6",
			},
			want: want{Enabled: true, MaxExecutors: new(int32(6))},
		},
		"spec.executor.instances wins over spark.executor.instances": {
			instances: new(int32(3)),
			sparkConf: map[string]string{"spark.executor.instances": "5"},
			want:      want{ExecutorInstances: new(int32(3))},
		},
		"enabled is case-insensitive in its value, as in Spark": {
			sparkConf: map[string]string{"spark.dynamicAllocation.enabled": "TRUE"},
			want:      want{Enabled: true},
		},
		// Spark property names are case-sensitive: a lowercase key has no effect in Spark.
		"a key in the wrong case is ignored": {
			sparkConf: map[string]string{"spark.dynamicallocation.enabled": "true"},
		},
		"a malformed or negative count is unset": {
			sparkConf: map[string]string{
				"spark.dynamicAllocation.enabled":      "true",
				"spark.dynamicAllocation.minExecutors": "two",
				"spark.dynamicAllocation.maxExecutors": "-1",
			},
			want: want{Enabled: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app := &SparkApplication{}
			app.Spec.DynamicAllocation = tc.typed
			app.Spec.Executor.Instances = tc.instances
			app.Spec.SparkConf = tc.sparkConf
			da := app.dynamicAllocation()
			got := want{
				Enabled:           da.enabled,
				MinExecutors:      da.minExecutors,
				MaxExecutors:      da.maxExecutors,
				InitialExecutors:  da.initialExecutors,
				ExecutorInstances: app.executorInstances(),
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("dynamicAllocation() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
