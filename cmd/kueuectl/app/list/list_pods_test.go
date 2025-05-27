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

package list

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	restfake "k8s.io/client-go/rest/fake"
	"k8s.io/utils/strings/slices"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuecmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

type podTestCase struct {
	name       string
	job        runtime.Object
	pods       []corev1.Pod
	mapperGVKs []schema.GroupVersionKind
	args       []string
	wantOut    string
	wantOutErr string
	wantErr    error
}

func TestPodCmd(t *testing.T) {
	testStartTime := time.Now()
	basePod := testingpod.MakePod("", metav1.NamespaceDefault).
		CreationTimestamp(testStartTime.Add(-time.Hour).Truncate(time.Second))

	testCases := []podTestCase{
		{
			name: "list pods of batch/job with wide output",
			job: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind: "Job",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						batchv1.JobNameLabel: "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(batchv1.JobNameLabel, "test-job").
					Obj(),
				*basePod.Clone().
					Name("valid-pod-2").
					Label(batchv1.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "job/test-job", "-o", "wide"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE         IP       NODE     NOMINATED NODE   READINESS GATES
valid-pod-1   1/1     Running   0          <unknown>   <none>   <none>   <none>           <none>
valid-pod-2   1/1     Running   0          <unknown>   <none>   <none>   <none>           <none>
`,
		}, {
			name: "list pods for valid batch/job type",
			job: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind: "Job",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						batchv1.JobNameLabel: "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(batchv1.JobNameLabel, "test-job").
					Obj(),
				*basePod.Clone().
					Name("valid-pod-2").
					Label(batchv1.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "job/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
valid-pod-2   1/1     Running   0          <unknown>
`,
		}, {
			name: "no valid pods for batch/job type in current namespace",
			job: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind: "Job",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						batchv1.JobNameLabel: "test-job",
					},
				},
			},
			pods: []corev1.Pod{},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args:    []string{"--for", "job/test-job"},
			wantOut: "",
			wantOutErr: `No resources found in default namespace.
`,
		}, {
			name: "no valid pods for batch/job type in all namespaces",
			job: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind: "Job",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						batchv1.JobNameLabel: "test-job",
					},
				},
			},
			pods: []corev1.Pod{},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args:    []string{"--for", "job/test-job", "-A"},
			wantOut: "",
			wantOutErr: `No resources found.
`,
		}, {
			name: "valid pods for batch/job type in all namespaces",
			job: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind: "Job",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sample-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						batchv1.JobNameLabel: "sample-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Namespace("dev-team-a").
					Label(batchv1.JobNameLabel, "sample-job").
					Obj(),
				*basePod.Clone().
					Name("valid-pod-2").
					Namespace("dev-team-b").
					Label(batchv1.JobNameLabel, "sample-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "job/sample-job", "-A"},
			wantOut: `NAMESPACE    NAME          READY   STATUS    RESTARTS   AGE
dev-team-a   valid-pod-1   1/1     Running   0          <unknown>
dev-team-b   valid-pod-2   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for kubeflow.org/PyTorchJob type",
			job: &kftraining.PyTorchJob{
				TypeMeta: metav1.TypeMeta{
					Kind: "PyTorchJob",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						kftraining.OperatorNameLabel: "pytorchjob-controller",
						kftraining.JobNameLabel:      "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(kftraining.OperatorNameLabel, "pytorchjob-controller").
					Label(kftraining.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "PyTorchJob",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "pytorchjob/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for kubeflow.org/paddlejob type",
			job: &kftraining.PaddleJob{
				TypeMeta: metav1.TypeMeta{
					Kind: "PaddleJob",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						kftraining.OperatorNameLabel: "paddlejob-controller",
						kftraining.JobNameLabel:      "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(kftraining.OperatorNameLabel, "paddlejob-controller").
					Label(kftraining.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "PaddleJob",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "paddlejob/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for kubeflow.org/tfjob type",
			job: &kftraining.TFJob{
				TypeMeta: metav1.TypeMeta{
					Kind: "TFJob",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						kftraining.OperatorNameLabel: "tfjob-controller",
						kftraining.JobNameLabel:      "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(kftraining.OperatorNameLabel, "tfjob-controller").
					Label(kftraining.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "TFJob",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "tfjob/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for kubeflow.org/mpijob type",
			job: &kftraining.MPIJob{
				TypeMeta: metav1.TypeMeta{
					Kind: "MPIJob",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						kftraining.OperatorNameLabel: "mpijob-controller",
						kftraining.JobNameLabel:      "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(kftraining.OperatorNameLabel, "mpijob-controller").
					Label(kftraining.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v2beta1",
					Kind:    "MPIJob",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "mpijob/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for kubeflow.org/xgboostjob type",
			job: &kftraining.XGBoostJob{
				TypeMeta: metav1.TypeMeta{
					Kind: "XGBoostJob",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						kftraining.OperatorNameLabel: "xgboostjob-controller",
						kftraining.JobNameLabel:      "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(kftraining.OperatorNameLabel, "xgboostjob-controller").
					Label(kftraining.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "XGBoostJob",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "xgboostjob/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for ray.io/rayjob type",
			job: &rayv1.RayJob{
				TypeMeta: metav1.TypeMeta{
					Kind: "RayJob",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						batchv1.JobNameLabel: "test-job",
					},
				},
				Status: rayv1.RayJobStatus{RayClusterName: "test-cluster"},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(batchv1.JobNameLabel, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "ray.io",
					Version: "v1",
					Kind:    "RayJob",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "rayjob/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for ray.io/raycluster type",
			job: &rayv1.RayCluster{
				TypeMeta: metav1.TypeMeta{
					Kind: "RayCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						rayutils.RayClusterLabelKey: "test-cluster",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(rayutils.RayClusterLabelKey, "test-cluster").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "ray.io",
					Version: "v1",
					Kind:    "RayCluster",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "raycluster/test-cluster"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods for jobset.x-k8s.io/jobset type",
			job: &jobsetapi.JobSet{
				TypeMeta: metav1.TypeMeta{
					Kind: "JobSet",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						jobsetapi.JobSetNameKey: "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(jobsetapi.JobSetNameKey, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "jobset/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		}, {
			name: "list pods with api-group filter",
			job: &jobsetapi.JobSet{
				TypeMeta: metav1.TypeMeta{
					Kind: "JobSet",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						jobsetapi.JobSetNameKey: "test-job",
					},
				},
			},
			pods: []corev1.Pod{
				*basePod.Clone().
					Name("valid-pod-1").
					Label(jobsetapi.JobSetNameKey, "test-job").
					Obj(),
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
				}, {
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			},
			args: []string{"--for", "jobset.jobset.x-k8s.io/test-job"},
			wantOut: `NAME          READY   STATUS    RESTARTS   AGE
valid-pod-1   1/1     Running   0          <unknown>
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			mapper := func() *meta.DefaultRESTMapper {
				m := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
				for _, gvk := range tc.mapperGVKs {
					m.Add(gvk, meta.RESTScopeNamespace)
				}
				return m
			}()

			scheme, err := buildTestRuntimeScheme()
			if err != nil {
				t.Errorf("Unexpected error\n%s", err)
			}

			codec := serializer.NewCodecFactory(scheme).LegacyCodec(scheme.PrioritizedVersionsAllGroups()...)

			restClient, err := mockRESTClient(codec, tc)
			if err != nil {
				t.Fatal(err)
			}

			tcg := kueuecmdtesting.NewTestClientGetter().
				WithNamespace(metav1.NamespaceDefault).
				WithRESTMapper(mapper).
				WithRESTClient(restClient)

			cmd := NewPodCmd(tcg, streams)
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

func buildTestRuntimeScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	scheme.AddKnownTypes(metav1.SchemeGroupVersion, &metav1.Table{})
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := rayv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := kftraining.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := jobsetapi.AddToScheme(scheme); err != nil {
		return nil, err
	}

	return scheme, nil
}

func mockRESTClient(codec runtime.Codec, tc podTestCase) (*restfake.RESTClient, error) {
	var podRespBody io.ReadCloser

	podList := &corev1.PodList{Items: tc.pods}
	if len(podList.Items) == 0 {
		podRespBody = emptyTableObjBody(codec)
	} else {
		podRespBody = podTableObjBody(codec, podList.Items...)
	}

	reqPathPrefix := fmt.Sprintf("/namespaces/%s", metav1.NamespaceDefault)
	if slices.Contains(tc.args, "-A") || slices.Contains(tc.args, "all-namespaces") {
		reqPathPrefix = ""
	}

	reqJobKind := strings.ToLower(tc.job.GetObjectKind().GroupVersionKind().Kind) + "s"

	var err error
	mockRestClient := &restfake.RESTClient{
		NegotiatedSerializer: resource.UnstructuredPlusDefaultContentConfig().NegotiatedSerializer,
		Client: restfake.CreateHTTPClient(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case fmt.Sprintf("%s/%s", reqPathPrefix, reqJobKind):
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     getDefaultHeader(),
					Body:       io.NopCloser(strings.NewReader(runtime.EncodeOrDie(codec, tc.job))),
				}, nil
			case fmt.Sprintf("%s/pods", reqPathPrefix):
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     getDefaultHeader(),
					Body:       podRespBody,
				}, nil
			default:
				err = fmt.Errorf("request URL: %#v, and request: %#v", request.URL, request)
				return nil, nil
			}
		}),
	}

	return mockRestClient, err
}

func getDefaultHeader() http.Header {
	header := http.Header{}
	header.Set("Content-Type", runtime.ContentTypeJSON)
	return header
}

var podColumns = []metav1.TableColumnDefinition{
	{Name: "Name", Type: "string", Format: "name"},
	{Name: "Ready", Type: "string", Format: ""},
	{Name: "Status", Type: "string", Format: ""},
	{Name: "Restarts", Type: "integer", Format: ""},
	{Name: "Age", Type: "string", Format: ""},
	{Name: "IP", Type: "string", Format: "", Priority: 1},
	{Name: "Node", Type: "string", Format: "", Priority: 1},
	{Name: "Nominated Node", Type: "string", Format: "", Priority: 1},
	{Name: "Readiness Gates", Type: "string", Format: "", Priority: 1},
}

// podTableObjBody builds a table with the given list of pods
func podTableObjBody(codec runtime.Codec, pods ...corev1.Pod) io.ReadCloser {
	table := &metav1.Table{
		TypeMeta:          metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: podColumns,
	}

	for i := range pods {
		b := bytes.NewBuffer(nil)
		_ = codec.Encode(&pods[i], b)
		table.Rows = append(table.Rows, metav1.TableRow{
			Object: runtime.RawExtension{Raw: b.Bytes()},
			Cells:  []any{pods[i].Name, "1/1", "Running", int64(0), "<unknown>", "<none>", "<none>", "<none>", "<none>"},
		})
	}

	data, err := json.Marshal(table)
	if err != nil {
		panic(err)
	}
	if !strings.Contains(string(data), `"meta.k8s.io/v1"`) {
		panic("expected v1, got " + string(data))
	}
	return io.NopCloser(bytes.NewReader(data))
}

// emptyTableObjBody builds an empty table response
func emptyTableObjBody(codec runtime.Codec) io.ReadCloser {
	table := &metav1.Table{
		ColumnDefinitions: podColumns,
	}
	return io.NopCloser(strings.NewReader(runtime.EncodeOrDie(codec, table)))
}
