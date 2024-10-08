/*
Copyright 2024 The Kubernetes Authors.

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

package jobframework_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

func toMPIJob(o runtime.Object) jobframework.GenericJob {
	return (*mpijob.MPIJob)(o.(*kfmpi.MPIJob))
}

func TestBaseWebhookDefault(t *testing.T) {
	testcases := map[string]struct {
		job                        *kfmpi.MPIJob
		manageJobsWithoutQueueName bool
		want                       *kfmpi.MPIJob
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job:  testingutil.MakeMPIJob("job", "default").Queue("queue").Suspend(false).Obj(),
			want: testingutil.MakeMPIJob("job", "default").Queue("queue").Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			job:                        testingutil.MakeMPIJob("job", "default").Suspend(false).Obj(),
			manageJobsWithoutQueueName: true,
			want:                       testingutil.MakeMPIJob("job", "default").Obj(),
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			w := &jobframework.BaseWebhook{
				ManageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				FromObject:                 toMPIJob,
			}
			if err := w.Default(context.Background(), tc.job); err != nil {
				t.Errorf("set defaults to a kubeflow/mpijob by a Defaulter")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
