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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestObservedRemoteObjectPreservesAdapterPreconditions(t *testing.T) {
	cases := map[string]struct {
		preconditions client.Preconditions
		wantConflict  bool
	}{
		"no adapter preconditions":     {},
		"matching UID":                 {preconditions: client.Preconditions{UID: new(types.UID("observed"))}},
		"conflicting UID":              {preconditions: client.Preconditions{UID: new(types.UID("different"))}, wantConflict: true},
		"conflicting resource version": {preconditions: client.Preconditions{ResourceVersion: new("different")}, wantConflict: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			original := testingjob.MakeJob("job", "ns").UID("observed").Obj()
			base := utiltesting.NewFakeClient(original)
			observed := &metav1.PartialObjectMetadata{}
			observed.SetGroupVersionKind(batchv1.SchemeGroupVersion.WithKind("Job"))
			if err := base.Get(ctx, client.ObjectKey{Name: "job", Namespace: "ns"}, observed); err != nil {
				t.Fatal(err)
			}
			cl := &observedRemoteObjectClient{Client: base, observed: observed}
			err := cl.Delete(ctx, testingjob.MakeJob("job", "ns").Obj(), tc.preconditions)
			if apierrors.IsConflict(err) != tc.wantConflict || (!tc.wantConflict && err != nil) {
				t.Fatalf("Delete() error = %v, wantConflict %v", err, tc.wantConflict)
			}
			err = base.Get(ctx, client.ObjectKey{Name: "job", Namespace: "ns"}, &batchv1.Job{})
			if tc.wantConflict && err != nil {
				t.Fatalf("conflicting adapter preconditions deleted the object: %v", err)
			}
			if !tc.wantConflict && !apierrors.IsNotFound(err) {
				t.Fatalf("object was not deleted: %v", err)
			}
		})
	}
}
