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
	"errors"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
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
		tolerateDeleting           bool
		wantSuspend                bool
		wantErr                    error
		skipAncestorGateOff        bool
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
		"job with ownerReference to a deleted known parent while terminating (GC teardown)": {
			obj: utiltestingjob.MakeJob("test-job", managedNamespace.Name).
				OwnerReference("nonexistent-parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				DeletionTimestamp(time.Now()).
				Finalizers("batch.kubernetes.io/job-tracking").
				Obj(),
			manageJobsWithoutQueueName: true,
			tolerateDeleting:           true,
			// With WithDeletingObjectTolerance (webhook call sites), the suspend check is
			// skipped entirely for an object that is being deleted, so the missing parent
			// is never looked up and no suspend is defaulted.
			wantSuspend: false,
		},
		"job with ownerReference to a deleted known parent while terminating, without tolerance": {
			obj: utiltestingjob.MakeJob("test-job", managedNamespace.Name).
				OwnerReference("nonexistent-parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				DeletionTimestamp(time.Now()).
				Finalizers("batch.kubernetes.io/job-tracking").
				Obj(),
			manageJobsWithoutQueueName: true,
			// Without WithDeletingObjectTolerance (reconciler predicates), a missing owner
			// fails hard even for a terminating object and even with the gate enabled.
			wantErr: ErrWorkloadOwnerNotFound,
		},
		"job with ownerReference to a deleted known parent while terminating, gate disabled": {
			obj: utiltestingjob.MakeJob("test-job", managedNamespace.Name).
				OwnerReference("nonexistent-parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				DeletionTimestamp(time.Now()).
				Finalizers("batch.kubernetes.io/job-tracking").
				Obj(),
			manageJobsWithoutQueueName: true,
			tolerateDeleting:           true,
			skipAncestorGateOff:        true,
			// With SkipAncestorCheckForDeletedWorkloads disabled, the previous behavior is
			// restored: a missing owner fails hard even for a terminating object.
			wantErr: ErrWorkloadOwnerNotFound,
		},
		"job with ownerReference to a missing parent while not terminating (cache lag)": {
			obj: utiltestingjob.MakeJob("test-job", managedNamespace.Name).
				OwnerReference("nonexistent-parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			manageJobsWithoutQueueName: true,
			wantErr:                    ErrWorkloadOwnerNotFound,
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

			features.SetFeatureGateDuringTest(t, features.SkipAncestorCheckForDeletedWorkloads, !tc.skipAncestorGateOff)
			var opts []WorkloadShouldBeSuspendedOption
			if tc.tolerateDeleting {
				opts = append(opts, WithDeletingObjectTolerance(true))
			}
			suspend, err := WorkloadShouldBeSuspended(ctx, tc.obj, client, tc.manageJobsWithoutQueueName, namespaceSelector, opts...)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Unexpected error: got %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && suspend != tc.wantSuspend {
				t.Errorf("Unexpected result: got %v wanted %v", suspend, tc.wantSuspend)
			}
		})
	}
}

// TestApplyDefaultLocalQueue covers the function this branch released, which
// has no namespace filtering of its own.
func TestApplyDefaultLocalQueue(t *testing.T) {
	cases := map[string]struct {
		job               *batchv1.Job
		defaultQueueExist bool
		wantQueueLabel    string
	}{
		"a job with no queue gets the default one": {
			job:               utiltestingjob.MakeJob("test-job", "ns").Obj(),
			defaultQueueExist: true,
			wantQueueLabel:    "default",
		},
		"an existing queue is not overwritten": {
			job:               utiltestingjob.MakeJob("test-job", "ns").Queue("other").Obj(),
			defaultQueueExist: true,
			wantQueueLabel:    "other",
		},
		"no default queue in the namespace": {
			job:            utiltestingjob.MakeJob("test-job", "ns").Obj(),
			wantQueueLabel: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ApplyDefaultLocalQueue(tc.job, func(string) bool { return tc.defaultQueueExist })

			if got := tc.job.Labels[constants.QueueLabel]; got != tc.wantQueueLabel {
				t.Errorf("queue label: got %q, want %q", got, tc.wantQueueLabel)
			}
		})
	}
}

// The namespace is read only when the label is about to be set. A job that is
// not a candidate must not need it, or a namespace the webhook cannot read
// would start failing admission.
func TestApplyDefaultLocalQueueWithManagedJobsNamespaceSelectorSkipsNamespaceRead(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					return errors.New("the namespace was read")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	cases := map[string]struct {
		job               *batchv1.Job
		defaultQueueExist bool
	}{
		"no default queue in the namespace": {
			job: utiltestingjob.MakeJob("test-job", "ns").Obj(),
		},
		"the job already names a queue": {
			job:               utiltestingjob.MakeJob("test-job", "ns").Queue("other").Obj(),
			defaultQueueExist: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ApplyDefaultLocalQueueWithManagedJobsNamespaceSelector(ctx, cl, tc.job,
				func(string) bool { return tc.defaultQueueExist }, labels.Everything())
			if err != nil {
				t.Errorf("ApplyDefaultLocalQueueWithManagedJobsNamespaceSelector() = %v, want no error", err)
			}
		})
	}
}

func TestApplyDefaultLocalQueueWithManagedJobsNamespaceSelector(t *testing.T) {
	managedNamespace := utiltesting.MakeNamespaceWrapper("managed-ns").Label(corev1.LabelMetadataName, "managed-ns").Obj()
	unmanagedNamespace := utiltesting.MakeNamespaceWrapper("unmanaged-ns").Label(corev1.LabelMetadataName, "unmanaged-ns").Obj()
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
		job            *batchv1.Job
		wantQueueLabel string
	}{
		"job in managed namespace gets default queue label": {
			job:            utiltestingjob.MakeJob("test-job", managedNamespace.Name).Obj(),
			wantQueueLabel: "default",
		},
		"job in unmanaged namespace does not get default queue label": {
			job:            utiltestingjob.MakeJob("test-job", unmanagedNamespace.Name).Obj(),
			wantQueueLabel: "",
		},
		"job in managed namespace with existing queue label is not overwritten": {
			job:            utiltestingjob.MakeJob("test-job", managedNamespace.Name).Queue("other").Obj(),
			wantQueueLabel: "other",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			builder.WithObjects(managedNamespace, unmanagedNamespace)
			cl := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			defaultQueueExist := func(ns string) bool {
				return true
			}

			if err := ApplyDefaultLocalQueueWithManagedJobsNamespaceSelector(ctx, cl, tc.job, defaultQueueExist, namespaceSelector); err != nil {
				t.Fatalf("ApplyDefaultLocalQueueWithManagedJobsNamespaceSelector() returned error: %v", err)
			}

			got := tc.job.Labels[constants.QueueLabel]
			if got != tc.wantQueueLabel {
				t.Errorf("queue label: got %q, want %q", got, tc.wantQueueLabel)
			}
		})
	}
}

func TestApplyDefaultWorkloadPriorityClass(t *testing.T) {
	t.Cleanup(EnableIntegrationsForTest(t, "batch/job"))
	parent := utiltestingjob.MakeJob("parent", "default").UID("parent").Queue("default").Obj()

	defaultWPC := &kueue.WorkloadPriorityClass{
		ObjectMeta: metav1.ObjectMeta{Name: constants.DefaultWorkloadPriorityClassName},
		Value:      100,
	}

	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}

	cases := map[string]struct {
		job                    client.Object
		wpcObjects             []client.Object
		featureGates           map[featuregate.Feature]bool
		wantPriorityClassLabel string
	}{
		"feature gate enabled, no label, default WPC exists": {
			job:                    utiltestingjob.MakeJob("test-job", "default").Obj(),
			wpcObjects:             []client.Object{defaultWPC},
			featureGates:           map[featuregate.Feature]bool{features.WorkloadPriorityClassDefaulting: true},
			wantPriorityClassLabel: constants.DefaultWorkloadPriorityClassName,
		},
		"feature gate disabled, no label, default WPC exists": {
			job:                    utiltestingjob.MakeJob("test-job", "default").Obj(),
			wpcObjects:             []client.Object{defaultWPC},
			featureGates:           map[featuregate.Feature]bool{features.WorkloadPriorityClassDefaulting: false},
			wantPriorityClassLabel: "",
		},
		"feature gate enabled, label already set": {
			job:                    utiltestingjob.MakeJob("test-job", "default").WorkloadPriorityClass("high").Obj(),
			wpcObjects:             []client.Object{defaultWPC},
			featureGates:           map[featuregate.Feature]bool{features.WorkloadPriorityClassDefaulting: true},
			wantPriorityClassLabel: "high",
		},
		"feature gate enabled, no label, default WPC does not exist": {
			job:                    utiltestingjob.MakeJob("test-job", "default").Obj(),
			wpcObjects:             nil,
			featureGates:           map[featuregate.Feature]bool{features.WorkloadPriorityClassDefaulting: true},
			wantPriorityClassLabel: "",
		},
		"feature gate enabled, owner managed by kueue": {
			job: utiltestingjob.MakeJob("test-job", "default").
				OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wpcObjects:             []client.Object{defaultWPC},
			featureGates:           map[featuregate.Feature]bool{features.WorkloadPriorityClassDefaulting: true},
			wantPriorityClassLabel: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if len(tc.wpcObjects) > 0 {
				builder = builder.WithObjects(tc.wpcObjects...)
			}
			k8sClient := builder.Build()
			ApplyDefaultWorkloadPriorityClass(ctx, k8sClient, tc.job)
			got := tc.job.GetLabels()[constants.WorkloadPriorityClassLabel]
			if got != tc.wantPriorityClassLabel {
				t.Errorf("unexpected priority class label: got %q, want %q", got, tc.wantPriorityClassLabel)
			}
		})
	}
}
