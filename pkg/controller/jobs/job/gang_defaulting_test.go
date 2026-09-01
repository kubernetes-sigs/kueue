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
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type fakeServerVersion string

func (v fakeServerVersion) GetServerVersion() versionutil.Version {
	return *versionutil.MustParseSemantic(string(v))
}

func jobJSON(t *testing.T, job *batchv1.Job, scheduling map[string]any) []byte {
	t.Helper()
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(job)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: content}
	u.SetGroupVersionKind(gvk)
	if scheduling != nil {
		if err := unstructured.SetNestedMap(u.Object, scheduling, "spec", "scheduling"); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := u.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func eligibleJob() *testingutil.JobWrapper {
	return testingutil.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Parallelism(3).Completions(3)
}

func TestGangDefaultSkipReason(t *testing.T) {
	v137 := versionutil.MustParseSemantic("1.37.0")
	testcases := map[string]struct {
		job           *batchv1.Job
		mutate        func(*batchv1.Job)
		scheduling    map[string]any
		serverVersion *versionutil.Version
		featureGates  map[featuregate.Feature]bool
		wantSkipped   bool
	}{
		"eligible": {
			job:           eligibleJob().Obj(),
			serverVersion: v137,
		},
		"unknown server version": {
			job:         eligibleJob().Obj(),
			wantSkipped: true,
		},
		"server version below 1.37": {
			job:           eligibleJob().Obj(),
			serverVersion: versionutil.MustParseSemantic("1.36.4"),
			wantSkipped:   true,
		},
		"1.37 release candidate": {
			job:           eligibleJob().Obj(),
			serverVersion: versionutil.MustParseSemantic("1.37.0-rc.1"),
		},
		"1.37 distribution build": {
			job:           eligibleJob().Obj(),
			serverVersion: versionutil.MustParseSemantic("1.37.0-eks-a1b2c3"),
		},
		"pre-release below 1.37": {
			job:           eligibleJob().Obj(),
			serverVersion: versionutil.MustParseSemantic("1.36.4-gke.100"),
			wantSkipped:   true,
		},
		"MultiKueue dispatched copy": {
			job:           eligibleJob().Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			serverVersion: v137,
			wantSkipped:   true,
		},
		"explicit gang": {
			job:           eligibleJob().Obj(),
			scheduling:    map[string]any{"schedulingPolicy": map[string]any{"gang": map[string]any{}}},
			serverVersion: v137,
			wantSkipped:   true,
		},
		"explicit basic": {
			job:           eligibleJob().Obj(),
			scheduling:    map[string]any{"schedulingPolicy": map[string]any{"basic": map[string]any{}}},
			serverVersion: v137,
			wantSkipped:   true,
		},
		"disruptionMode alone": {
			job:           eligibleJob().Obj(),
			scheduling:    map[string]any{"disruptionMode": map[string]any{"all": map[string]any{}}},
			serverVersion: v137,
			wantSkipped:   true,
		},
		"Pod template brings its own PodGroup": {
			job: eligibleJob().Obj(),
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{
					PodGroupName: new("byo-group"),
				}
			},
			serverVersion: v137,
			wantSkipped:   true,
		},
		"schedulingConstraints alone": {
			job:           eligibleJob().Obj(),
			scheduling:    map[string]any{"schedulingConstraints": map[string]any{}},
			serverVersion: v137,
			wantSkipped:   true,
		},
		"empty spec.scheduling": {
			job:           eligibleJob().Obj(),
			scheduling:    map[string]any{},
			serverVersion: v137,
			wantSkipped:   true,
		},
		"parallelism 1": {
			job:           eligibleJob().Parallelism(1).Completions(1).Obj(),
			serverVersion: v137,
			wantSkipped:   true,
		},
		"completions unset": {
			job:           testingutil.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Parallelism(3).Obj(),
			serverVersion: v137,
			wantSkipped:   true,
		},
		"completions below parallelism": {
			job:           eligibleJob().Completions(2).Obj(),
			serverVersion: v137,
			wantSkipped:   true,
		},
		"completions above parallelism": {
			job:           eligibleJob().Completions(4).Obj(),
			serverVersion: v137,
			wantSkipped:   true,
		},
		"NonIndexed is eligible": {
			job:           eligibleJob().Indexed(false).Obj(),
			serverVersion: v137,
		},
		"workload slices": {
			job:           eligibleJob().SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).Obj(),
			serverVersion: v137,
			featureGates:  map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wantSkipped:   true,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			if tc.mutate != nil {
				tc.mutate(tc.job)
			}
			obj := &unstructured.Unstructured{}
			if err := obj.UnmarshalJSON(jobJSON(t, tc.job, tc.scheduling)); err != nil {
				t.Fatal(err)
			}
			reason := gangDefaultSkipReason(tc.job, obj.Object, tc.serverVersion)
			if got := reason != ""; got != tc.wantSkipped {
				t.Errorf("gangDefaultSkipReason() skipped=%t, want %t (reason %q)", got, tc.wantSkipped, reason)
			}
		})
	}
}

func TestServerVersionUnknown(t *testing.T) {
	testcases := map[string]serverVersionGetter{
		"no getter":         nil,
		"fetch not yet run": kubeversion.NewServerVersionFetcher(nil),
	}
	for name, getter := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &JobWebhook{kubeServerVersion: getter}
			if got := wh.serverVersion(); got != nil {
				t.Errorf("serverVersion() = %v, want nil", got)
			}
		})
	}
}

func TestGangDefaultingHandler(t *testing.T) {
	testcases := map[string]struct {
		job           *batchv1.Job
		scheduling    map[string]any
		typedPatches  []jsonpatch.Operation
		gateEnabled   bool
		serverVersion string
		wantDefaulted bool
	}{
		"defaults an eligible managed Job": {
			job:           eligibleJob().Obj(),
			gateEnabled:   true,
			serverVersion: "1.37.0",
			wantDefaulted: true,
		},
		"gate disabled": {
			job:           eligibleJob().Obj(),
			serverVersion: "1.37.0",
		},
		"not managed by Kueue": {
			job:           testingutil.MakeJob("job", metav1.NamespaceDefault).Parallelism(3).Completions(3).Obj(),
			gateEnabled:   true,
			serverVersion: "1.37.0",
		},
		"queue name added by the typed defaulter counts": {
			job: testingutil.MakeJob("job", metav1.NamespaceDefault).Parallelism(3).Completions(3).Obj(),
			typedPatches: []jsonpatch.Operation{
				jsonpatch.NewOperation("add", "/metadata/labels", map[string]any{"kueue.x-k8s.io/queue-name": "queue"}),
			},
			gateEnabled:   true,
			serverVersion: "1.37.0",
			wantDefaulted: true,
		},
		"user-set spec.scheduling is left alone": {
			job:           eligibleJob().Obj(),
			scheduling:    map[string]any{"schedulingPolicy": map[string]any{"basic": map[string]any{}}},
			gateEnabled:   true,
			serverVersion: "1.37.0",
		},
		"server below 1.37": {
			job:           eligibleJob().Obj(),
			gateEnabled:   true,
			serverVersion: "1.36.4",
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.BatchJobGangSchedulingByDefault, tc.gateEnabled)
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace(metav1.NamespaceDefault)).Build()
			cqCache := schdcache.New(cl)
			integrationManager := newTestIntegrationManager(t)
			t.Cleanup(integrationManager.EnableIntegrationsForTest(t, "batch/job"))
			wh := &JobWebhook{
				integrationManager:           integrationManager,
				client:                       cl,
				managedJobsNamespaceSelector: labels.Everything(),
				queues:                       qcache.NewManagerForUnitTests(cl, cqCache),
				cache:                        cqCache,
				kubeServerVersion:            fakeServerVersion(tc.serverVersion),
			}
			typed := admission.HandlerFunc(func(context.Context, admission.Request) admission.Response {
				return admission.Patched("", tc.typedPatches...)
			})
			handler := &gangDefaultingHandler{typed: typed, webhook: wh}

			raw := jobJSON(t, tc.job, tc.scheduling)
			req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				Object:    runtime.RawExtension{Raw: raw},
			}}
			resp := handler.Handle(ctx, req)
			if !resp.Allowed {
				t.Fatalf("request denied: %v", resp.Result)
			}
			got, err := applyJSONPatches(raw, resp.Patches)
			if err != nil {
				t.Fatal(err)
			}
			want, err := applyJSONPatches(raw, tc.typedPatches)
			if err != nil {
				t.Fatal(err)
			}
			wantObj := &unstructured.Unstructured{}
			if err := wantObj.UnmarshalJSON(want); err != nil {
				t.Fatal(err)
			}
			if tc.wantDefaulted {
				// Spelled out rather than taken from gangSchedulingDefault(), so
				// that a change to the policy Kueue writes fails here.
				want := map[string]any{
					"schedulingPolicy": map[string]any{"gang": map[string]any{}},
					"disruptionMode":   map[string]any{"all": map[string]any{}},
				}
				if err := unstructured.SetNestedMap(wantObj.Object, want, "spec", "scheduling"); err != nil {
					t.Fatal(err)
				}
				annotations := wantObj.GetAnnotations()
				if annotations == nil {
					annotations = map[string]string{}
				}
				annotations[GangDefaultedAnnotation] = "true"
				wantObj.SetAnnotations(annotations)
			}
			gotObj := &unstructured.Unstructured{}
			if err := gotObj.UnmarshalJSON(got); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(wantObj.Object, gotObj.Object); diff != "" {
				t.Errorf("unexpected Job after admission (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGangDefaultingHandlerIsIdempotent(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.BatchJobGangSchedulingByDefault, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace(metav1.NamespaceDefault)).Build()
	cqCache := schdcache.New(cl)
	integrationManager := newTestIntegrationManager(t)
	t.Cleanup(integrationManager.EnableIntegrationsForTest(t, "batch/job"))
	wh := &JobWebhook{
		integrationManager:           integrationManager,
		client:                       cl,
		managedJobsNamespaceSelector: labels.Everything(),
		queues:                       qcache.NewManagerForUnitTests(cl, cqCache),
		cache:                        cqCache,
		kubeServerVersion:            fakeServerVersion("1.37.0"),
	}
	handler := &gangDefaultingHandler{
		typed: admission.HandlerFunc(func(context.Context, admission.Request) admission.Response {
			return admission.Allowed("")
		}),
		webhook: wh,
	}
	raw := jobJSON(t, eligibleJob().Obj(), nil)
	first := handler.Handle(ctx, admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: raw},
	}})
	defaulted, err := applyJSONPatches(raw, first.Patches)
	if err != nil {
		t.Fatal(err)
	}
	second := handler.Handle(ctx, admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: defaulted},
	}})
	if len(second.Patches) != 0 {
		encoded, _ := json.Marshal(second.Patches)
		t.Errorf("reinvocation produced patches: %s", encoded)
	}
}
