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
