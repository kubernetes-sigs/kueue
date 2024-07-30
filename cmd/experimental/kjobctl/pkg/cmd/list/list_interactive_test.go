/*
Copyright 2024 The Kubernetes Authors.

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

package list

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
	testingclock "k8s.io/utils/clock/testing"

	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestInteractiveCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print only kjobctl pods": {
			ns: "ns1",
			objs: []runtime.Object{
				wrappers.MakePod("i1", "ns1").
					Profile("profile1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("j2", "ns2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with namespace filter": {
			ns: "ns1",
			objs: []runtime.Object{
				wrappers.MakePod("i1", "ns1").
					Profile("profile1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("j2", "ns2").
					Profile("profile2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with profile filter": {
			args: []string{"--profile", "profile1"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with profile filter (short flag)": {
			args: []string{"-p", "profile1"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with localqueue filter": {
			args: []string{"--localqueue", "localqueue1"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with localqueue filter (short flag)": {
			args: []string{"-q", "localqueue1"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with label selector filter": {
			args: []string{"--selector", "foo=bar"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					Label("foo", "bar").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with label selector filter (short flag)": {
			args: []string{"-l", "foo=bar"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					Label("foo", "bar").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print interactive list with field selector filter": {
			args: []string{"--field-selector", "metadata.name=i1"},
			objs: []runtime.Object{
				wrappers.MakePod("i1", metav1.NamespaceDefault).
					Profile("profile1").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
				wrappers.MakePod("i2", metav1.NamespaceDefault).
					Profile("profile2").
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Phase(corev1.PodSucceeded).
					Obj(),
			},
			wantOut: `NAME   PROFILE    STATUS      AGE
i1     profile1   Succeeded   60m
`,
		},
		"should print not found error": {
			wantOutErr: fmt.Sprintf("No resources found in %s namespace.\n", metav1.NamespaceDefault),
		},
		"should print not found error with all-namespaces filter": {
			args:       []string{"-A"},
			wantOutErr: "No resources found\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			clientset := fake.NewSimpleClientset(tc.objs...)
			clientset.PrependReactor("list", "pods", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				listAction := action.(kubetesting.ListActionImpl)
				fieldsSelector := listAction.GetListRestrictions().Fields

				obj, err := clientset.Tracker().List(listAction.GetResource(), listAction.GetKind(), listAction.Namespace)
				podList := obj.(*corev1.PodList)

				filtered := make([]corev1.Pod, 0, len(podList.Items))
				for _, item := range podList.Items {
					fieldsSet := fields.Set{
						"metadata.name": item.Name,
					}
					if fieldsSelector.Matches(fieldsSet) {
						filtered = append(filtered, item)
					}
				}
				podList.Items = filtered
				return true, podList, err
			})

			tcg := cmdtesting.NewTestClientGetter().WithK8sClientset(clientset)
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewInteractiveCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
