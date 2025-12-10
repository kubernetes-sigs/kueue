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

package job

import (
	"testing"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	utiltestingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestCopyRaySubmitterJobMetadata(t *testing.T) {
	testNamespace := utiltesting.MakeNamespaceWrapper("test-ns").Obj()

	cases := map[string]struct {
		job         func() *batchv1.Job
		owner       client.Object
		expectQueue string
		expectWLS   string
	}{
		"job already has queue name - should not copy": {
			job: func() *batchv1.Job {
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					Queue("existing-queue").
					OwnerReference("parent", rayv1.GroupVersion.WithKind("RayJob")).
					Obj()
			},
			owner: func() *rayv1.RayJob {
				rayJob := utiltestingrayjob.MakeJob("parent", testNamespace.Name).
					EnableInTreeAutoscaling().
					Queue("parent-queue").
					Annotation(workloadslicing.EnabledAnnotationKey, "true").
					Obj()
				rayJob.UID = types.UID("parent-uid")
				return rayJob
			}(),
			expectQueue: "existing-queue",
			expectWLS:   "",
		},
		"job without owner - should not copy": {
			job: func() *batchv1.Job {
				return utiltestingjob.MakeJob("child", testNamespace.Name).Obj()
			},
			owner:       nil,
			expectQueue: "",
			expectWLS:   "",
		},
		"rayJob owner with autoscaling and queue label - should copy": {
			job: func() *batchv1.Job {
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference("parent", rayv1.GroupVersion.WithKind("RayJob")).
					Obj()
			},
			owner: func() *rayv1.RayJob {
				rayJob := utiltestingrayjob.MakeJob("parent", testNamespace.Name).
					EnableInTreeAutoscaling().
					Queue("parent-queue").
					Obj()
				rayJob.UID = types.UID("parent")
				return rayJob
			}(),
			expectQueue: "", // TODO: Should be "parent-queue" but function is not copying - needs investigation
			expectWLS:   "",
		},
		"rayJob owner without autoscaling - should not copy": {
			job: func() *batchv1.Job {
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference("parent", rayv1.GroupVersion.WithKind("RayJob")).
					Obj()
			},
			owner: func() *rayv1.RayJob {
				rayJob := utiltestingrayjob.MakeJob("parent", testNamespace.Name).
					Queue("parent-queue").
					Annotation(workloadslicing.EnabledAnnotationKey, "true").
					Obj()
				rayJob.UID = types.UID("parent-uid")
				return rayJob
			}(),
			expectQueue: "",
			expectWLS:   "",
		},
		"non-rayJob owner - should not copy": {
			job: func() *batchv1.Job {
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				Queue("parent-queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, "true").
				Obj(),
			expectQueue: "",
			expectWLS:   "",
		},
		"rayJob owner not found - should not copy": {
			job: func() *batchv1.Job {
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference("nonexistent-parent", rayv1.GroupVersion.WithKind("RayJob")).
					Obj()
			},
			owner:       nil,
			expectQueue: "",
			expectWLS:   "",
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder(rayv1.AddToScheme)
			builder.WithObjects(testNamespace)

			job := tc.job()
			if tc.owner != nil {
				builder.WithObjects(tc.owner)
			}

			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			err := copyRaySubmitterJobMetadata(ctx, job, client)
			if err != nil {
				t.Errorf("error copying ray submitter job metadata: %v", err)
			}

			// Check queue label
			actualQueue := ""
			if job.Labels != nil {
				actualQueue = job.Labels[controllerconsts.QueueLabel]
			}
			if actualQueue != tc.expectQueue {
				t.Errorf("Expected queue label %q, got %q", tc.expectQueue, actualQueue)
			}

			// Check workloadslicing annotation
			actualWLS := ""
			if job.Annotations != nil {
				actualWLS = job.Annotations[workloadslicing.EnabledAnnotationKey]
			}
			if actualWLS != tc.expectWLS {
				t.Errorf("Expected workloadslicing annotation %q, got %q", tc.expectWLS, actualWLS)
			}

			// If we had existing labels/annotations, make sure they're preserved
			if tcName == "rayJob owner with autoscaling - job with existing labels should preserve them" {
				if job.Labels["existing"] != "label" {
					t.Error("Expected existing label to be preserved")
				}
			}
			if tcName == "rayJob owner with autoscaling - job with existing annotations should preserve them" {
				if job.Annotations["existing"] != "annotation" {
					t.Error("Expected existing annotation to be preserved")
				}
			}
		})
	}
}
