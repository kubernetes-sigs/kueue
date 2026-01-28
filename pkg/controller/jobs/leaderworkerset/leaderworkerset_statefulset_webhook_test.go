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

package leaderworkerset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestSetupStatefulSetWebhookDefault(t *testing.T) {
	testCases := map[string]struct {
		sts  *appsv1.StatefulSet
		want *appsv1.StatefulSet
	}{
		"without leaderworkerset.sigs.k8s.io/name": {
			sts:  statefulset.MakeStatefulSet("test-lws", metav1.NamespaceDefault).Obj(),
			want: statefulset.MakeStatefulSet("test-lws", metav1.NamespaceDefault).Obj(),
		},
		"with leaderworkerset.sigs.k8s.io/name": {
			sts: statefulset.MakeStatefulSet("test-lws", metav1.NamespaceDefault).
				Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
				Obj(),
			want: statefulset.MakeStatefulSet("test-lws", metav1.NamespaceDefault).
				Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
				Annotation(constants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			w := &StatefulSetWebhook{}

			if err := w.Default(ctx, tc.sts); err != nil {
				t.Errorf("Failed to set defaults for appsv1/statefulsets: %s", err)
			}

			if diff := cmp.Diff(tc.want, tc.sts); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
