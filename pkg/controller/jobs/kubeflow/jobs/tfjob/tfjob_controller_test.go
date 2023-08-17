/*
Copyright 2023 The Kubernetes Authors.

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

package tfjob

import (
	"testing"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	v1 "k8s.io/api/core/v1"
)

func TestCalcPriorityClassName(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.TFJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.TFJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "chief-priority",
								},
							},
						},
						kftraining.TFJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "scheduling-priority",
		},
		"runPolicy present, but without priority; fallback to chief": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "chief-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "chief-priority",
		},
		"specified on chief takes precedence over chief": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "chief-priority",
								},
							},
						},
						kftraining.TFJobReplicaTypePS: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "ps-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "chief-priority",
		},
		"chief present, but without priority; fallback to evaluator": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
							},
						},
						kftraining.TFJobReplicaTypeEval: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "eval-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "eval-priority",
		},
		"specified on worker only": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {},
						kftraining.TFJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "worker-priority",
		},
		"evaluator present, but without priority; fallback to empty": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {},
						kftraining.TFJobReplicaTypeEval: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
							},
						},
					},
				},
			},
			wantPriorityClassName: "",
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			tfJob := fromObject(&tc.job)
			gotPriorityClassName := tfJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}
