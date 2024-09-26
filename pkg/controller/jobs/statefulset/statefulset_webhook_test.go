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

package statefulset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		statefulset                *appsv1.StatefulSet
		manageJobsWithoutQueueName bool
		enableIntegrations         []string
		want                       *appsv1.StatefulSet
	}{
		"pod with queue": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecPodGroupNameLabel("", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(10).
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				Obj(),
		},
		"statefulset without replicas": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecPodGroupNameLabel("", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(1).
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &Webhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			if err := w.Default(ctx, tc.statefulset); err != nil {
				t.Errorf("failed to set defaults for v1/statefulset: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.statefulset); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
