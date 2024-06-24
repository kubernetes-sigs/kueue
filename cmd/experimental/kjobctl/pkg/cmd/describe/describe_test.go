package describe

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	restfake "k8s.io/client-go/rest/fake"
	"k8s.io/utils/ptr"

	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
)

func TestDescribeCmd(t *testing.T) {
	testCases := map[string]struct {
		args        []string
		argsFormat  int
		objs        []runtime.Object
		mapperKinds []schema.GroupVersionKind
		wantOut     string
		wantOutErr  string
		wantErr     error
	}{
		"describe specific task with 'mode task' format": {
			args:       []string{"job", "sample-job-5zd6r"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sample-job-5zd6r",
						Namespace: "default",
						Labels: map[string]string{
							"kjobctl.x-k8s.io/localqueue": "lq1",
							"kjobctl.x-k8s.io/profile":    "profile3",
							"kueue.x-k8s.io/queue-name":   "user-queue",
						},
						Annotations: map[string]string{},
					},
					Spec: batchv1.JobSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"batch.kubernetes.io/controller-uid": "50e605e2-8a87-4e6c-9f54-a0086717497a",
							},
						},
						Parallelism:    ptr.To[int32](3),
						Completions:    ptr.To[int32](2),
						CompletionMode: ptr.To(batchv1.NonIndexedCompletion),
						Suspend:        ptr.To(false),
						BackoffLimit:   ptr.To[int32](6),
					},
					Status: batchv1.JobStatus{
						Active:         0,
						Succeeded:      2,
						Failed:         0,
						StartTime:      ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
						CompletionTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 33, 0, time.UTC)),
					},
				},
			},
			wantOut: `Name:             sample-job-5zd6r
Namespace:        default
Selector:         batch.kubernetes.io/controller-uid=50e605e2-8a87-4e6c-9f54-a0086717497a
Labels:           kjobctl.x-k8s.io/localqueue=lq1
                  kjobctl.x-k8s.io/profile=profile3
                  kueue.x-k8s.io/queue-name=user-queue
Annotations:      <none>
Parallelism:      3
Completions:      2
Completion Mode:  NonIndexed
Suspend:          false
Backoff Limit:    6
Start Time:       Mon, 01 Jan 2024 00:00:00 +0000
Completed At:     Mon, 01 Jan 2024 00:00:33 +0000
Duration:         33s
Pods Statuses:    0 Active / 2 Succeeded / 0 Failed
`,
		},
		"describe specific task with 'mode slash task' format": {
			args:       []string{"job/sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sample-job-8c7zt",
						Namespace: "default",
						Labels: map[string]string{
							"kjobctl.x-k8s.io/localqueue": "lq2",
							"kjobctl.x-k8s.io/profile":    "profile1",
							"kueue.x-k8s.io/queue-name":   "user-queue",
						},
						Annotations: map[string]string{},
					},
					Spec: batchv1.JobSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"batch.kubernetes.io/controller-uid": "3bcc4673-ccc0-45dc-ae65-5f06043a2f6c",
							},
						},
						Parallelism:    ptr.To[int32](3),
						Completions:    ptr.To[int32](2),
						CompletionMode: ptr.To(batchv1.NonIndexedCompletion),
						Suspend:        ptr.To(false),
						BackoffLimit:   ptr.To[int32](6),
					},
					Status: batchv1.JobStatus{
						Active:         0,
						Succeeded:      2,
						Failed:         0,
						StartTime:      ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
						CompletionTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 33, 0, time.UTC)),
					},
				},
			},
			wantOut: `Name:             sample-job-8c7zt
Namespace:        default
Selector:         batch.kubernetes.io/controller-uid=3bcc4673-ccc0-45dc-ae65-5f06043a2f6c
Labels:           kjobctl.x-k8s.io/localqueue=lq2
                  kjobctl.x-k8s.io/profile=profile1
                  kueue.x-k8s.io/queue-name=user-queue
Annotations:      <none>
Parallelism:      3
Completions:      2
Completion Mode:  NonIndexed
Suspend:          false
Backoff Limit:    6
Start Time:       Mon, 01 Jan 2024 00:00:00 +0000
Completed At:     Mon, 01 Jan 2024 00:00:33 +0000
Duration:         33s
Pods Statuses:    0 Active / 2 Succeeded / 0 Failed
`,
		},
		"describe all tasks": {
			args:       []string{"job"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				&batchv1.JobList{
					Items: []batchv1.Job{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "sample-job-5zd6r",
								Namespace: "default",
								Labels: map[string]string{
									"kjobctl.x-k8s.io/localqueue": "lq1",
									"kjobctl.x-k8s.io/profile":    "profile3",
									"kueue.x-k8s.io/queue-name":   "user-queue",
								},
								Annotations: map[string]string{},
							},
							Spec: batchv1.JobSpec{
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"batch.kubernetes.io/controller-uid": "50e605e2-8a87-4e6c-9f54-a0086717497a",
									},
								},
								Parallelism:    ptr.To[int32](3),
								Completions:    ptr.To[int32](2),
								CompletionMode: ptr.To(batchv1.NonIndexedCompletion),
								Suspend:        ptr.To(false),
								BackoffLimit:   ptr.To[int32](6),
							},
							Status: batchv1.JobStatus{
								Active:         0,
								Succeeded:      2,
								Failed:         0,
								StartTime:      ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
								CompletionTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 33, 0, time.UTC)),
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "sample-job-8c7zt",
								Namespace: "default",
								Labels: map[string]string{
									"kjobctl.x-k8s.io/localqueue": "lq2",
									"kjobctl.x-k8s.io/profile":    "profile1",
									"kueue.x-k8s.io/queue-name":   "user-queue",
								},
								Annotations: map[string]string{},
							},
							Spec: batchv1.JobSpec{
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"batch.kubernetes.io/controller-uid": "3bcc4673-ccc0-45dc-ae65-5f06043a2f6c",
									},
								},
								Parallelism:    ptr.To[int32](3),
								Completions:    ptr.To[int32](2),
								CompletionMode: ptr.To(batchv1.NonIndexedCompletion),
								Suspend:        ptr.To(false),
								BackoffLimit:   ptr.To[int32](6),
							},
							Status: batchv1.JobStatus{
								Active:         0,
								Succeeded:      2,
								Failed:         0,
								StartTime:      ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
								CompletionTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 33, 0, time.UTC)),
							},
						},
					},
				},
			},
			wantOut: `Name:             sample-job-5zd6r
Namespace:        default
Selector:         batch.kubernetes.io/controller-uid=50e605e2-8a87-4e6c-9f54-a0086717497a
Labels:           kjobctl.x-k8s.io/localqueue=lq1
                  kjobctl.x-k8s.io/profile=profile3
                  kueue.x-k8s.io/queue-name=user-queue
Annotations:      <none>
Parallelism:      3
Completions:      2
Completion Mode:  NonIndexed
Suspend:          false
Backoff Limit:    6
Start Time:       Mon, 01 Jan 2024 00:00:00 +0000
Completed At:     Mon, 01 Jan 2024 00:00:33 +0000
Duration:         33s
Pods Statuses:    0 Active / 2 Succeeded / 0 Failed


Name:             sample-job-8c7zt
Namespace:        default
Selector:         batch.kubernetes.io/controller-uid=3bcc4673-ccc0-45dc-ae65-5f06043a2f6c
Labels:           kjobctl.x-k8s.io/localqueue=lq2
                  kjobctl.x-k8s.io/profile=profile1
                  kueue.x-k8s.io/queue-name=user-queue
Annotations:      <none>
Parallelism:      3
Completions:      2
Completion Mode:  NonIndexed
Suspend:          false
Backoff Limit:    6
Start Time:       Mon, 01 Jan 2024 00:00:00 +0000
Completed At:     Mon, 01 Jan 2024 00:00:33 +0000
Duration:         33s
Pods Statuses:    0 Active / 2 Succeeded / 0 Failed
`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TZ", "")
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter()

			if len(tc.mapperKinds) != 0 {
				mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
				for _, k := range tc.mapperKinds {
					mapper.Add(k, meta.RESTScopeNamespace)
				}
				tcg.WithRESTMapper(mapper)
			}

			if len(tc.objs) != 0 {
				scheme := runtime.NewScheme()

				if err := batchv1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}

				codec := serializer.NewCodecFactory(scheme).LegacyCodec(scheme.PrioritizedVersionsAllGroups()...)
				tcg.WithRESTClient(&restfake.RESTClient{
					NegotiatedSerializer: resource.UnstructuredPlusDefaultContentConfig().NegotiatedSerializer,
					Resp: &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(runtime.EncodeOrDie(codec, tc.objs[0])))),
					},
				})
			}

			cmd := NewDescribeCmd(tcg, streams)
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
