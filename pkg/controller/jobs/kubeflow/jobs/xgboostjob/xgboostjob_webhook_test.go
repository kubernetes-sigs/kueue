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

package xgboostjob

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"

	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"
)

func TestDefault(t *testing.T) {
	testcases := map[string]struct {
		job                        *kftraining.XGBoostJob
		manageJobsWithoutQueueName bool
		want                       *kftraining.XGBoostJob
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job:  testingutil.MakeXGBoostJob("job", "default").Queue("queue").Suspend(false).Obj(),
			want: testingutil.MakeXGBoostJob("job", "default").Queue("queue").Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			job:                        testingutil.MakeXGBoostJob("job", "default").Suspend(false).Obj(),
			manageJobsWithoutQueueName: true,
			want:                       testingutil.MakeXGBoostJob("job", "default").Obj(),
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			w := &XGBoostJobWebhook{manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName}
			if err := w.Default(context.Background(), tc.job); err != nil {
				t.Errorf("set defaults to a kubeflow.org/xgboostjob by a Defaulter")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
