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

package multikueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

func TestSyncReplacedSliceTerminalStatusGuards(t *testing.T) {
	terminalJob := utiltestingrayjob.MakeJob("job1", TestNamespace).
		PrebuiltWorkloadLabel("wl1").
		Label(kueue.MultiKueueOriginLabel, defaultOrigin).
		JobStatus(rayv1.JobStatusSucceeded).
		JobDeploymentStatus(rayv1.JobDeploymentStatusComplete)

	tests := map[string]struct {
		framework         string
		clusterName       *string
		connected         bool
		workerJob         *rayv1.RayJob
		workerGetError    error
		managerPatchError error
		wantWorkerGets    int
		wantStatusPatches int
		wantStatus        rayv1.RayJobStatus
		wantError         error
	}{
		"missing worker cluster name": {},
		"empty worker cluster name": {
			clusterName: new(""),
		},
		"unconfigured worker cluster": {
			clusterName: new("worker2"), connected: true,
		},
		"disconnected worker": {
			clusterName: new("worker1"),
		},
		"adapter without terminal sync is skipped": {
			framework: "batch/job", clusterName: new("worker1"), connected: true,
		},
		"adapter with terminal recovery disabled is skipped": {
			framework: "ray.io/raycluster", clusterName: new("worker1"), connected: true,
		},
		"missing worker job is ignored": {
			clusterName: new("worker1"), connected: true,
			wantWorkerGets: 1,
		},
		"connected worker propagates terminal status": {
			clusterName: new("worker1"), connected: true,
			workerJob:         terminalJob.DeepCopy(),
			wantWorkerGets:    1,
			wantStatusPatches: 1,
			wantStatus:        terminalJob.Status,
		},
		"nonterminal worker is ignored": {
			clusterName: new("worker1"), connected: true,
			workerJob: terminalJob.Clone().
				JobStatus(rayv1.JobStatusRunning).
				JobDeploymentStatus(rayv1.JobDeploymentStatusRunning).Obj(),
			wantWorkerGets: 1,
		},
		"worker ownership rejection propagates": {
			clusterName: new("worker1"), connected: true,
			workerJob:      terminalJob.Clone().Label(kueue.MultiKueueOriginLabel, "another-origin").Obj(),
			wantWorkerGets: 1,
			wantError:      jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
		},
		"worker get failure is wrapped": {
			clusterName: new("worker1"), connected: true,
			workerGetError: errFake,
			wantWorkerGets: 1,
			wantError:      errFake,
		},
		"manager status patch failure is wrapped": {
			clusterName: new("worker1"), connected: true,
			workerJob:         terminalJob.DeepCopy(),
			managerPatchError: errFake,
			wantWorkerGets:    1,
			wantStatusPatches: 1,
			wantError:         errFake,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			framework := tc.framework
			if framework == "" {
				framework = rayjob.FrameworkName
			}
			integration, ok := jobs.NewIntegrationManager().GetIntegration(framework)
			if !ok || integration.MultiKueueAdapter == nil {
				t.Fatalf("missing MultiKueue adapter for %q", framework)
			}
			adapter := integration.MultiKueueAdapter

			managerJob := utiltestingrayjob.MakeJob("job1", TestNamespace).Obj()
			managerJob.UID = "uid1"
			statusPatches := 0
			managerClient := getClientBuilder(ctx).
				WithObjects(
					managerJob,
					utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
					utiltestingapi.MakeAdmissionCheck("ac1").
						ControllerName(kueue.MultiKueueControllerName).
						Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
						Obj(),
				).
				WithStatusSubresource(managerJob).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						statusPatches++
						if tc.managerPatchError != nil {
							return tc.managerPatchError
						}
						return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			helper, err := admissioncheck.NewMultiKueueStoreHelper(managerClient)
			if err != nil {
				t.Fatal(err)
			}

			workerBuilder := getClientBuilder(ctx)
			if tc.workerJob != nil {
				workerBuilder.WithObjects(tc.workerJob.DeepCopy())
			}
			workerGets := 0
			workerClient := workerBuilder.WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					workerGets++
					if tc.workerGetError != nil {
						return tc.workerGetError
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()
			beforeWorkerJobs := &rayv1.RayJobList{}
			if err := workerClient.List(ctx, beforeWorkerJobs); err != nil {
				t.Fatal(err)
			}

			clusters := newClustersReconciler(managerClient, TestNamespace)
			remote := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "", nil)
			remote.client = NewNeverCachingClient(workerClient)
			if tc.connected {
				remote.connState.markConnected()
			} else {
				remote.connState.markDisconnected(time.Now())
			}
			clusters.remoteClients["worker1"] = remote
			reconciler := &wlReconciler{
				client:   managerClient,
				helper:   helper,
				clusters: clusters,
				origin:   defaultOrigin,
			}
			wl := utiltestingapi.MakeWorkload("wl1", TestNamespace).
				ControllerReference(adapter.GVK(), "job1", "uid1").Obj()
			wl.Status.ClusterName = tc.clusterName

			if err := reconciler.syncReplacedSliceTerminalStatus(ctx, wl, "ac1", adapter); !errors.Is(err, tc.wantError) {
				t.Errorf("terminal status sync error = %v, want %v", err, tc.wantError)
			}
			if workerGets != tc.wantWorkerGets {
				t.Errorf("worker GET calls = %d, want %d", workerGets, tc.wantWorkerGets)
			}
			if statusPatches != tc.wantStatusPatches {
				t.Errorf("manager status PATCH calls = %d, want %d", statusPatches, tc.wantStatusPatches)
			}
			gotManagerJob := &rayv1.RayJob{}
			if err := managerClient.Get(ctx, client.ObjectKeyFromObject(managerJob), gotManagerJob); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.wantStatus, gotManagerJob.Status); diff != "" {
				t.Errorf("unexpected manager status (-want/+got):\n%s", diff)
			}
			afterWorkerJobs := &rayv1.RayJobList{}
			if err := workerClient.List(ctx, afterWorkerJobs); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(beforeWorkerJobs, afterWorkerJobs); diff != "" {
				t.Errorf("worker jobs changed during terminal status sync (-before/+after):\n%s", diff)
			}
		})
	}
}
