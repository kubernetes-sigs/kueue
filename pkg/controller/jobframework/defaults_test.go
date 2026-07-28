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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func TestApplyDefaultLocalQueue(t *testing.T) {
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

			if err := ApplyDefaultLocalQueue(ctx, cl, tc.job, defaultQueueExist, namespaceSelector); err != nil {
				t.Fatalf("ApplyDefaultLocalQueue() returned error: %v", err)
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
