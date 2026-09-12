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

package pod

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

type replacingPodReadClient struct {
	client.Client
	replacement *corev1.Pod
}

func (c *replacingPodReadClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, typedPod := obj.(*corev1.Pod); typedPod && c.replacement != nil {
		old := testingpod.MakePod(key.Name, key.Namespace).Obj()
		if err := c.Delete(ctx, old); err != nil {
			return err
		}
		if err := c.Create(ctx, c.replacement); err != nil {
			return err
		}
		c.replacement = nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestDeleteOwnedRemotePodGroup(t *testing.T) {
	cases := map[string]struct {
		replacement  *corev1.Pod
		wantConflict bool
		wantNames    []string
	}{
		"remote sibling UIDs differ from manager UIDs": {wantNames: []string{}},
		"representative replaced before adapter reads the pod group": {
			replacement:  testingpod.MakePod("pod-a", "ns").UID("new").Label(podconstants.GroupNameLabel, "other-group").Label(kueue.MultiKueueOriginLabel, "origin").Obj(),
			wantConflict: true,
			wantNames:    []string{"pod-a", "pod-b"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			manager := utiltesting.NewClientBuilder().WithIndex(&corev1.Pod{}, PodGroupNameCacheKey, IndexPodGroupName).WithObjects(
				testingpod.MakePod("pod-a", "ns").UID("manager-a").Label(podconstants.GroupNameLabel, "group").Obj(),
				testingpod.MakePod("pod-b", "ns").UID("manager-b").Label(podconstants.GroupNameLabel, "group").Obj(),
			).Build()
			remote := &replacingPodReadClient{Client: utiltesting.NewFakeClient(
				testingpod.MakePod("pod-a", "ns").UID("remote-a").Label(podconstants.GroupNameLabel, "group").Label(kueue.MultiKueueOriginLabel, "origin").Obj(),
				testingpod.MakePod("pod-b", "ns").UID("remote-b").Label(podconstants.GroupNameLabel, "group").Label(kueue.MultiKueueOriginLabel, "origin").Obj(),
			), replacement: tc.replacement}
			err := jobframework.DeleteRemoteObjectIfOwned(ctx, manager, remote, &multiKueueAdapter{}, client.ObjectKey{Name: "pod-a", Namespace: "ns"}, "origin", "")
			if apierrors.IsConflict(err) != tc.wantConflict || (!tc.wantConflict && err != nil) {
				t.Fatalf("delete error = %v, wantConflict %v", err, tc.wantConflict)
			}
			remaining := &corev1.PodList{}
			if err := remote.List(ctx, remaining); err != nil {
				t.Fatal(err)
			}
			if len(remaining.Items) != len(tc.wantNames) {
				t.Fatalf("remaining pods = %v, want %v", remaining.Items, tc.wantNames)
			}
			for i, pod := range remaining.Items {
				if pod.Name != tc.wantNames[i] {
					t.Fatalf("remaining pod = %q, want %q", pod.Name, tc.wantNames[i])
				}
			}
		})
	}
}
