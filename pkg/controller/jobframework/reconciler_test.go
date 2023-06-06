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

package jobframework

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

func TestIsParentJobManaged(t *testing.T) {
	parentJobName := "test-job-parent"
	childJobName := "test-job-child"
	jobNamespace := "default"
	cases := map[string]struct {
		parentJob   client.Object
		job         client.Object
		wantManaged bool
		wantErr     error
	}{
		"child job doesn't have ownerReference": {
			parentJob: testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				Obj(),
			wantErr: errChildJobOwnerNotFound,
		},
		"child job has ownerReference with unknown workload owner": {
			parentJob: testingjob.MakeJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantErr: errUnknownWorkloadOwner,
		},
		"child job has ownerReference with known non-existing workload owner": {
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kubeflow.SchemeGroupVersionKind).
				Obj(),
			wantErr: errWorkloadOwnerNotFound,
		},
		"child job has ownerReference with known existing workload owner, and the parent job has queue-name label": {
			parentJob: testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Queue("test-q").
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kubeflow.SchemeGroupVersionKind).
				Obj(),
			wantManaged: true,
		},
		"child job has ownerReference with known existing workload owner, and the parent job doesn't has queue-name label": {
			parentJob: testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kubeflow.SchemeGroupVersionKind).
				Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder(kubeflow.AddToScheme)
			if tc.parentJob != nil {
				builder = builder.WithObjects(tc.parentJob)
			}
			cl := builder.Build()
			r := JobReconciler{client: cl}
			got, gotErr := r.isParentJobManaged(context.Background(), tc.job, jobNamespace)
			if tc.wantManaged != got {
				t.Errorf("Unexpected response from isParentManaged want: %v,got: %v", tc.wantManaged, got)
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
