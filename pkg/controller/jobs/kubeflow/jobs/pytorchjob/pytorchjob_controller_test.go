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

package pytorchjob

import (
	"testing"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	v1 "k8s.io/api/core/v1"
)

func TestCalcPriorityClassName(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.PyTorchJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.PyTorchJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
		"runPolicy present, but without priority; fallback to master": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "master-priority",
		},
		"specified on master takes precedence over worker": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "master-priority",
		},
		"master present, but without priority; fallback to worker": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
							},
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
		"specified on worker only": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
		"worker present, but without priority; fallback to empty": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
			pytorchJob := fromObject(&tc.job)
			gotPriorityClassName := pytorchJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}
