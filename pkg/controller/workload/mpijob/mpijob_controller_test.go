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

package mpijob

import (
	"testing"

	common "github.com/kubeflow/common/pkg/apis/common/v1"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	v1 "k8s.io/api/core/v1"
)

func TestCalcPriorityClassName(t *testing.T) {
	testcases := map[string]struct {
		job                   kubeflow.MPIJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kubeflow.MPIJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					RunPolicy: kubeflow.RunPolicy{
						SchedulingPolicy: &kubeflow.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
						kubeflow.MPIReplicaTypeWorker: {
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
		"runPolicy present, but without priority; fallback to launcher": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					RunPolicy: kubeflow.RunPolicy{
						SchedulingPolicy: &kubeflow.SchedulingPolicy{},
					},
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "launcher-priority",
		},
		"specified on launcher takes precedence over worker": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
						kubeflow.MPIReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "launcher-priority",
		},
		"launcher present, but without priority; fallback to worker": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
							},
						},
						kubeflow.MPIReplicaTypeWorker: {
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
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {},
						kubeflow.MPIReplicaTypeWorker: {
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
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {},
						kubeflow.MPIReplicaTypeWorker: {
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
			mpiJob := MPIJob{tc.job}
			gotPriorityClassName := mpiJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}
