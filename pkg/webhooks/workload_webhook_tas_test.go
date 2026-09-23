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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestValidateWorkloadUnhealthyNodesEvictionThreshold(t *testing.T) {
	baseWorkload := utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace)
	annotation := kueue.UnhealthyNodesConcurrentEvictionThresholdAnnotation
	invalidValue := func(value string) error {
		return field.ErrorList{
			field.Invalid(field.NewPath("metadata", "annotations").Key(annotation), value, "must be an integer between 1 and 8"),
		}.ToAggregate()
	}
	cases := map[string]struct {
		before, after *kueue.Workload
		wantErr       error
	}{
		"create without annotation": {
			after: baseWorkload.Clone().Obj(),
		},
		"create with minimum threshold": {
			after: baseWorkload.Clone().Annotation(annotation, "1").Obj(),
		},
		"create with intermediate threshold": {
			after: baseWorkload.Clone().Annotation(annotation, "3").Obj(),
		},
		"create with maximum threshold": {
			after: baseWorkload.Clone().Annotation(annotation, "8").Obj(),
		},
		"create with zero": {
			after:   baseWorkload.Clone().Annotation(annotation, "0").Obj(),
			wantErr: invalidValue("0"),
		},
		"create with negative threshold": {
			after:   baseWorkload.Clone().Annotation(annotation, "-1").Obj(),
			wantErr: invalidValue("-1"),
		},
		"create above maximum": {
			after:   baseWorkload.Clone().Annotation(annotation, "9").Obj(),
			wantErr: invalidValue("9"),
		},
		"create with empty value": {
			after:   baseWorkload.Clone().Annotation(annotation, "").Obj(),
			wantErr: invalidValue(""),
		},
		"create with non-numeric value": {
			after:   baseWorkload.Clone().Annotation(annotation, "many").Obj(),
			wantErr: invalidValue("many"),
		},
		"create with fractional value": {
			after:   baseWorkload.Clone().Annotation(annotation, "1.5").Obj(),
			wantErr: invalidValue("1.5"),
		},
		"create with whitespace": {
			after:   baseWorkload.Clone().Annotation(annotation, " 2 ").Obj(),
			wantErr: invalidValue(" 2 "),
		},
		"create with overflowing integer": {
			after:   baseWorkload.Clone().Annotation(annotation, "99999999999999999999").Obj(),
			wantErr: invalidValue("99999999999999999999"),
		},
		"add a valid annotation": {
			before: baseWorkload.Clone().Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "2").Obj(),
		},
		"add an empty annotation": {
			before:  baseWorkload.Clone().Obj(),
			after:   baseWorkload.Clone().Annotation(annotation, "").Obj(),
			wantErr: invalidValue(""),
		},
		"change a valid annotation to an invalid value": {
			before:  baseWorkload.Clone().Annotation(annotation, "2").Obj(),
			after:   baseWorkload.Clone().Annotation(annotation, "9").Obj(),
			wantErr: invalidValue("9"),
		},
		"change a legacy invalid annotation to another invalid value": {
			before:  baseWorkload.Clone().Annotation(annotation, "many").Obj(),
			after:   baseWorkload.Clone().Annotation(annotation, "0").Obj(),
			wantErr: invalidValue("0"),
		},
		"correct a legacy invalid annotation": {
			before: baseWorkload.Clone().Annotation(annotation, "many").Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "2").Obj(),
		},
		"remove a legacy invalid annotation": {
			before: baseWorkload.Clone().Annotation(annotation, "many").Obj(),
			after:  baseWorkload.Clone().Obj(),
		},
		"remove a valid annotation": {
			before: baseWorkload.Clone().Annotation(annotation, "2").Obj(),
			after:  baseWorkload.Clone().Obj(),
		},
		"update status with an unchanged legacy invalid annotation": {
			before: baseWorkload.Clone().Annotation(annotation, "many").Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "many").UnhealthyNodes("node1", "node2").Obj(),
		},
		"update status with an unchanged legacy empty annotation": {
			before: baseWorkload.Clone().Annotation(annotation, "").Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "").UnhealthyNodes("node1", "node2").Obj(),
		},
		"remove finalizers with an unchanged legacy invalid annotation": {
			before: baseWorkload.Clone().Annotation(annotation, "many").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "many").Obj(),
		},
		"status unhealthy nodes may exceed the annotation threshold": {
			before: baseWorkload.Clone().Annotation(annotation, "1").Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "1").UnhealthyNodes("node1", "node2").Obj(),
		},
		"lower threshold below the current unhealthy node count": {
			before: baseWorkload.Clone().Annotation(annotation, "2").UnhealthyNodes("node1", "node2").Obj(),
			after:  baseWorkload.Clone().Annotation(annotation, "1").UnhealthyNodes("node1", "node2").Obj(),
		},
	}
	for name, tc := range cases {
		for gateState, enabled := range map[string]bool{"gate enabled": true, "gate disabled": false} {
			t.Run(name+"/"+gateState, func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.TASReplaceMultipleFailedNodes, enabled)
				wh := &WorkloadWebhook{}
				var warnings admission.Warnings
				var err error
				if tc.before == nil {
					warnings, err = wh.ValidateCreate(t.Context(), tc.after.DeepCopy())
				} else {
					warnings, err = wh.ValidateUpdate(t.Context(), tc.before.DeepCopy(), tc.after.DeepCopy())
				}
				wantErr := tc.wantErr
				if !enabled {
					wantErr = nil
				}
				if diff := cmp.Diff(wantErr, err); diff != "" {
					t.Errorf("unexpected validation error (-want,+got):\n%s", diff)
				}
				if len(warnings) != 0 {
					t.Errorf("unexpected validation warnings: %v", warnings)
				}
			})
		}
	}
}
