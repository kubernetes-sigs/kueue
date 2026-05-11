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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestSanitizePodSets(t *testing.T) {
	testCases := map[string]struct {
		podSets         []kueue.PodSet
		expectedPodSets []kueue.PodSet
	}{
		"init containers and containers": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value1"}).
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					InitContainers(*utiltesting.MakeContainer().
						Name("init1").
						WithEnvVar(corev1.EnvVar{Name: "ENV2", Value: "value3"}).
						WithEnvVar(corev1.EnvVar{Name: "ENV2", Value: "value4"}).
						Obj()).
					Obj(),
			},
			expectedPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					InitContainers(*utiltesting.MakeContainer().
						Name("init1").
						WithEnvVar(corev1.EnvVar{Name: "ENV2", Value: "value4"}).
						Obj()).
					Obj(),
			},
		},
		"containers only": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value1"}).
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					Obj(),
			},
			expectedPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					Obj(),
			},
		},
		"empty podsets": {
			podSets:         []kueue.PodSet{},
			expectedPodSets: []kueue.PodSet{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			SanitizePodSets(tc.podSets)

			if diff := cmp.Diff(tc.expectedPodSets, tc.podSets); diff != "" {
				t.Errorf("unexpected difference: %s", diff)
			}
		})
	}
}

func TestHasStaleGangCompletions(t *testing.T) {
	cases := map[string]struct {
		job  *batchv1.Job
		want bool
	}{
		"gang indexed, succeeded>0: stale": {
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism:    ptr.To[int32](10),
					Completions:    ptr.To[int32](10),
					CompletionMode: ptr.To(batchv1.IndexedCompletion),
				},
				Status: batchv1.JobStatus{Succeeded: 2},
			},
			want: true,
		},
		"gang indexed, succeeded=0: not stale": {
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism:    ptr.To[int32](10),
					Completions:    ptr.To[int32](10),
					CompletionMode: ptr.To(batchv1.IndexedCompletion),
				},
				Status: batchv1.JobStatus{Succeeded: 0},
			},
			want: false,
		},
		"non-gang (parallelism < completions): not stale": {
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism:    ptr.To[int32](5),
					Completions:    ptr.To[int32](100),
					CompletionMode: ptr.To(batchv1.IndexedCompletion),
				},
				Status: batchv1.JobStatus{Succeeded: 50},
			},
			want: false,
		},
		"non-indexed gang: not stale": {
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](10),
					Completions: ptr.To[int32](10),
				},
				Status: batchv1.JobStatus{Succeeded: 2},
			},
			want: false,
		},
		"single pod: not stale": {
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism:    ptr.To[int32](1),
					Completions:    ptr.To[int32](1),
					CompletionMode: ptr.To(batchv1.IndexedCompletion),
				},
				Status: batchv1.JobStatus{Succeeded: 1},
			},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := hasStaleGangCompletions(tc.job); got != tc.want {
				t.Errorf("hasStaleGangCompletions() = %v, want %v", got, tc.want)
			}
		})
	}
}

func newFakeClientWithJobIndex(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&batchv1.Job{}).
		WithIndex(&batchv1.Job{}, indexer.OwnerReferenceUID, indexer.IndexOwnerUID).
		Build()
}

func TestDeleteStaleGangChildJobs(t *testing.T) {
	parentUID := types.UID("parent-uid-123")
	parent := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fake-parent",
			Namespace: "default",
			UID:       parentUID,
		},
	}
	ownerRef := []metav1.OwnerReference{{
		APIVersion: "jobset.x-k8s.io/v1alpha2",
		Kind:       "JobSet",
		Name:       "fake-parent",
		UID:        parentUID,
		Controller: ptr.To(true),
	}}

	makeChildJob := func(name string, parallelism, completions, succeeded int32, indexed bool) *batchv1.Job {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       "default",
				OwnerReferences: ownerRef,
			},
			Spec: batchv1.JobSpec{
				Parallelism: ptr.To(parallelism),
				Completions: ptr.To(completions),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers:    []corev1.Container{{Name: "c", Image: "pause"}},
					},
				},
			},
		}
		if indexed {
			job.Spec.CompletionMode = ptr.To(batchv1.IndexedCompletion)
		}
		job.Status.Succeeded = succeeded
		return job
	}

	cases := map[string]struct {
		childJobs     []*batchv1.Job
		wantRemaining []string
	}{
		"stale gang child deleted": {
			childJobs:     []*batchv1.Job{makeChildJob("stale-job", 10, 10, 2, true)},
			wantRemaining: nil,
		},
		"clean gang child not deleted": {
			childJobs:     []*batchv1.Job{makeChildJob("clean-job", 10, 10, 0, true)},
			wantRemaining: []string{"clean-job"},
		},
		"non-gang child not deleted": {
			childJobs:     []*batchv1.Job{makeChildJob("batch-job", 5, 100, 50, true)},
			wantRemaining: []string{"batch-job"},
		},
		"non-indexed child not deleted": {
			childJobs:     []*batchv1.Job{makeChildJob("non-indexed", 10, 10, 2, false)},
			wantRemaining: []string{"non-indexed"},
		},
		"mixed: only stale deleted": {
			childJobs: []*batchv1.Job{
				makeChildJob("stale-worker", 10, 10, 2, true),
				makeChildJob("clean-worker", 10, 10, 0, true),
			},
			wantRemaining: []string{"clean-worker"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			objs := make([]client.Object, len(tc.childJobs))
			for i, j := range tc.childJobs {
				objs[i] = j
			}
			cl := newFakeClientWithJobIndex(objs...)
			r := &JobReconciler{client: cl}

			if err := r.deleteStaleGangChildJobs(ctx, parent); err != nil {
				t.Fatalf("deleteStaleGangChildJobs() error: %v", err)
			}

			var remaining batchv1.JobList
			if err := cl.List(ctx, &remaining, client.InNamespace("default")); err != nil {
				t.Fatalf("List: %v", err)
			}

			gotSet := make(map[string]bool)
			for _, j := range remaining.Items {
				gotSet[j.Name] = true
			}
			wantSet := make(map[string]bool)
			for _, n := range tc.wantRemaining {
				wantSet[n] = true
			}

			for _, n := range tc.wantRemaining {
				if !gotSet[n] {
					t.Errorf("expected Job %q to remain, but it was deleted", n)
				}
			}
			for n := range gotSet {
				if !wantSet[n] {
					t.Errorf("expected Job %q to be deleted, but it remains", n)
				}
			}
		})
	}
}
