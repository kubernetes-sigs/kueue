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

package jobframework

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestWorkloadShouldBeSuspended(t *testing.T) {
	t.Cleanup(EnableIntegrationsForTest(t, "batch/job"))
	managedNamespace := utiltesting.MakeNamespaceWrapper("managed-ns").Label(corev1.LabelMetadataName, "managed-ns").Obj()
	unmanagedNamespace := utiltesting.MakeNamespaceWrapper("unmanaged-ns").Label(corev1.LabelMetadataName, "unmanaged-ns").Obj()
	parent := utiltestingjob.MakeJob("parent", managedNamespace.Name).UID("parent").Queue("default").Obj()
	ls := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{unmanagedNamespace.Name},
			},
		},
	}
	namespaceSelector, _ := metav1.LabelSelectorAsSelector(ls)

	cases := map[string]struct {
		obj                        client.Object
		manageJobsWithoutQueueName bool
		wantSuspend                bool
	}{
		"job with queue name ": {
			obj:                        utiltestingjob.MakeJob("test-job", managedNamespace.Name).Queue("default").Obj(),
			manageJobsWithoutQueueName: false,
			wantSuspend:                true,
		},
		"job with queue name manageJobs": {
			obj:                        utiltestingjob.MakeJob("test-job", managedNamespace.Name).Queue("default").Obj(),
			manageJobsWithoutQueueName: true,
			wantSuspend:                true,
		},
		"job without queue name": {
			obj:                        utiltestingjob.MakeJob("test-job", managedNamespace.Name).Obj(),
			manageJobsWithoutQueueName: false,
			wantSuspend:                false,
		},
		"job without queue name with manageJobs": {
			obj:                        utiltestingjob.MakeJob("test-job", managedNamespace.Name).Obj(),
			manageJobsWithoutQueueName: true,
			wantSuspend:                true,
		},
		"job without queue name but with managed parent with manageJobs": {
			obj: utiltestingjob.MakeJob("test-job", managedNamespace.Name).
				OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			manageJobsWithoutQueueName: true,
			wantSuspend:                false,
		},
		"job without queue name with manageJobs with feature disabled": {
			obj:                        utiltestingjob.MakeJob("test-job", managedNamespace.Name).Obj(),
			manageJobsWithoutQueueName: true,
			wantSuspend:                true,
		},
		"job without queue name with manageJobs in unmanaged ns": {
			obj:                        utiltestingjob.MakeJob("test-job", unmanagedNamespace.Name).Obj(),
			manageJobsWithoutQueueName: true,
			wantSuspend:                false,
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			builder.WithObjects(managedNamespace, unmanagedNamespace, tc.obj, parent)
			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			suspend, err := WorkloadShouldBeSuspended(ctx, tc.obj, client, tc.manageJobsWithoutQueueName, namespaceSelector)
			if err != nil {
				t.Errorf("Got error: %v", err)
			}
			if suspend != tc.wantSuspend {
				t.Errorf("Unexpected result: got %v wanted %v", suspend, tc.wantSuspend)
			}
		})
	}
}

func TestCopyLabelAndAnnotationFromOwner(t *testing.T) {
	t.Cleanup(EnableIntegrationsForTest(t, "batch/job"))

	testNamespace := utiltesting.MakeNamespaceWrapper("test-ns").Obj()

	cases := map[string]struct {
		job         func() *batchv1.Job
		owner       *batchv1.Job
		expectQueue string
		expectWLS   string
		expectError bool
	}{
		"job already has queue name - should not copy": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("parent", testNamespace.Name).UID("parent-uid").Obj()
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					Queue("existing-queue").
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				Queue("parent-queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, "true").
				Obj(),
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
		"owner has queue label - should copy": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("parent", testNamespace.Name).UID("parent-uid").Obj()
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				Queue("parent-queue").
				Obj(),
			expectQueue: "parent-queue",
			expectWLS:   "",
		},
		"owner has workloadslicing annotation - should copy": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("parent", testNamespace.Name).UID("parent-uid").Obj()
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, "true").
				Obj(),
			expectQueue: "",
			expectWLS:   "true",
		},
		"owner has both queue and workloadslicing - should copy both": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("parent", testNamespace.Name).UID("parent-uid").Obj()
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				Queue("parent-queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, "true").
				Obj(),
			expectQueue: "parent-queue",
			expectWLS:   "true",
		},
		"job with existing labels - should add queue label": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("parent", testNamespace.Name).UID("parent-uid").Obj()
				job := utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
				job.Labels = map[string]string{"existing": "label"}
				return job
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				Queue("parent-queue").
				Obj(),
			expectQueue: "parent-queue",
			expectWLS:   "",
		},
		"job with existing annotations - should add workloadslicing annotation": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("parent", testNamespace.Name).UID("parent-uid").Obj()
				job := utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
				job.Annotations = map[string]string{"existing": "annotation"}
				return job
			},
			owner: utiltestingjob.MakeJob("parent", testNamespace.Name).
				UID("parent-uid").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, "true").
				Obj(),
			expectQueue: "",
			expectWLS:   "true",
		},
		"owner not found - should not copy": {
			job: func() *batchv1.Job {
				parent := utiltestingjob.MakeJob("nonexistent-parent", testNamespace.Name).UID("parent-uid").Obj()
				return utiltestingjob.MakeJob("child", testNamespace.Name).
					OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			},
			owner:       nil,
			expectQueue: "",
			expectWLS:   "",
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			builder.WithObjects(testNamespace)

			job := tc.job()
			if tc.owner != nil {
				builder.WithObjects(tc.owner)
			}

			client := builder.Build()
			ctx, log := utiltesting.ContextWithLog(t)

			CopyLabelAndAnnotationFromOwner(ctx, job, client, log)

			// Check queue label
			actualQueue := ""
			if job.Labels != nil {
				actualQueue = job.Labels[constants.QueueLabel]
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
			if tcName == "job with existing labels - should add queue label" {
				if job.Labels["existing"] != "label" {
					t.Error("Expected existing label to be preserved")
				}
			}
			if tcName == "job with existing annotations - should add workloadslicing annotation" {
				if job.Annotations["existing"] != "annotation" {
					t.Error("Expected existing annotation to be preserved")
				}
			}
		})
	}
}
