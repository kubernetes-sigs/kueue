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
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

type changingRemoteJobClient struct {
	client.Client
	replacement   *batchv1.Job
	changeOrigin  bool
	deleteOptions client.DeleteOptions
}

func (c *changingRemoteJobClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deleteOptions.ApplyOptions(opts)
	if c.replacement != nil {
		if err := c.Client.Delete(ctx, obj); err != nil {
			return err
		}
		if err := c.Create(ctx, c.replacement); err != nil {
			return err
		}
	} else if c.changeOrigin {
		current := &batchv1.Job{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
			return err
		}
		current.Labels[kueue.MultiKueueOriginLabel] = "other"
		if err := c.Update(ctx, current); err != nil {
			return err
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func TestDeleteOwnedRemoteJobPreservesObservedIdentity(t *testing.T) {
	cases := map[string]struct {
		replacement  *batchv1.Job
		changeOrigin bool
		wantConflict bool
		wantJob      *batchv1.Job
	}{
		"normal deletion preserves adapter propagation": {},
		"ownership changes after the ownership check": {
			changeOrigin: true, wantConflict: true,
			wantJob: testingjob.MakeJob("job", "ns").UID("old").Label(kueue.MultiKueueOriginLabel, "other").Obj(),
		},
		"same-name replacement after the ownership check": {
			replacement:  testingjob.MakeJob("job", "ns").UID("new").Label(kueue.MultiKueueOriginLabel, "origin").Obj(),
			wantConflict: true,
			wantJob:      testingjob.MakeJob("job", "ns").UID("new").Label(kueue.MultiKueueOriginLabel, "origin").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			original := testingjob.MakeJob("job", "ns").UID("old").Label(kueue.MultiKueueOriginLabel, "origin").Obj()
			remote := &changingRemoteJobClient{Client: utiltesting.NewFakeClient(original), replacement: tc.replacement, changeOrigin: tc.changeOrigin}
			err := jobframework.DeleteRemoteObjectIfOwned(ctx, utiltesting.NewFakeClient(), remote, &multiKueueAdapter{}, client.ObjectKey{Name: "job", Namespace: "ns"}, "origin", "")
			if apierrors.IsConflict(err) != tc.wantConflict || (!tc.wantConflict && err != nil) {
				t.Fatalf("delete error = %v, wantConflict %v", err, tc.wantConflict)
			}
			if remote.deleteOptions.PropagationPolicy == nil || *remote.deleteOptions.PropagationPolicy != metav1.DeletePropagationBackground {
				t.Fatal("adapter propagation policy was lost")
			}
			got := &batchv1.Job{}
			err = remote.Get(ctx, client.ObjectKey{Name: "job", Namespace: "ns"}, got)
			if tc.wantJob == nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("job was not deleted: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got.UID != tc.wantJob.UID || got.Labels[kueue.MultiKueueOriginLabel] != tc.wantJob.Labels[kueue.MultiKueueOriginLabel] {
					t.Fatalf("unexpected preserved identity: %+v", got.ObjectMeta)
				}
			}
		})
	}
}
