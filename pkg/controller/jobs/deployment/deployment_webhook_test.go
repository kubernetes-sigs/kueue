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

package deployment

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		deployment                 *appsv1.Deployment
		manageJobsWithoutQueueName bool
		enableIntegrations         []string
		want                       *appsv1.Deployment
	}{
		"pod with queue": {
			enableIntegrations: []string{"pod"},
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &Webhook{
				client: cli,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			if err := w.Default(ctx, tc.deployment); err != nil {
				t.Errorf("failed to set defaults for v1/deployment: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.deployment); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
