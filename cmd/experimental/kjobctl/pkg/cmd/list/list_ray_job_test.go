/*
Copyright %s Apache License, Version 2.0 (the "License");
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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	kubetesting "k8s.io/client-go/testing"
	testingclock "k8s.io/utils/clock/testing"

	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestRayJobCmd(t *testing.T) {
	testStartTime := time.Now().Truncate(time.Second)

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print only kjobctl ray jobs": {
			args: []string{},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", "ns1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with namespace filter": {
			args: []string{},
			ns:   "ns1",
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", "ns1").
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", "ns2").
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with profile filter": {
			args: []string{"--profile", "profile1"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile2").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with profile filter (short flag)": {
			args: []string{"-p", "profile1"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile2").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with localqueue filter": {
			args: []string{"--localqueue", "localqueue1"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue2").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with localqueue filter (short filter)": {
			args: []string{"-q", "localqueue1"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue2").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with label selector filter": {
			args: []string{"--selector", "foo=bar"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Label("foo", "bar").
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with label selector filter (short flag)": {
			args: []string{"-l", "foo=bar"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Label("foo", "bar").
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print ray job list with field selector filter": {
			args: []string{"--field-selector", "metadata.name=rj1"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("localqueue1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
rj1    profile1   localqueue1   rc1                SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print not found error": {
			args:       []string{},
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
			clientset.PrependReactor("list", "rayjobs", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				listAction := action.(kubetesting.ListActionImpl)
				fieldsSelector := listAction.GetListRestrictions().Fields

				obj, err := clientset.Tracker().List(listAction.GetResource(), listAction.GetKind(), listAction.Namespace)
				rayJobList := obj.(*rayv1.RayJobList)

				filtered := make([]rayv1.RayJob, 0, len(rayJobList.Items))
				for _, item := range rayJobList.Items {
					fieldsSet := fields.Set{
						"metadata.name": item.Name,
					}
					if fieldsSelector.Matches(fieldsSet) {
						filtered = append(filtered, item)
					}
				}
				rayJobList.Items = filtered
				return true, rayJobList, err
			})

			tcg := cmdtesting.NewTestClientGetter().WithRayClientset(clientset)
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewRayJobCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
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
