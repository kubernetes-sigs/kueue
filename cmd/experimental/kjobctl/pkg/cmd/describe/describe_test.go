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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
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
		"describe job with 'mode task' format": {
			args:       []string{"job", "sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				getSampleJob("sample-job-8c7zt"),
			},
			wantOut: `Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
`,
		},
		"describe specific task with 'mode slash task' format": {
			args:       []string{"job/sample-job-8c7zt"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				getSampleJob("sample-job-8c7zt"),
			},
			wantOut: `Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
`,
		},
		"describe all jobs": {
			args:       []string{"job"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			objs: []runtime.Object{
				&batchv1.JobList{
					Items: []batchv1.Job{
						*getSampleJob("sample-job-5zd6r"),
						*getSampleJob("sample-job-8c7zt"),
					},
				},
			},
			wantOut: `Name:           sample-job-5zd6r
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>


Name:           sample-job-8c7zt
Namespace:      default
Labels:         kjobctl.x-k8s.io/profile=sample-profile
Parallelism:    3
Completions:    2
Start Time:     Mon, 01 Jan 2024 00:00:00 +0000
Completed At:   Mon, 01 Jan 2024 00:00:33 +0000
Duration:       33s
Pods Statuses:  0 Active / 2 Succeeded / 0 Failed
Pod Template:
  Containers:
   sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      sleep
      15s
    Args:
      30s
    Requests:
      cpu:        1
      memory:     200Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
`,
		},
		"describe interactive with 'mode task' format": {
			args:       []string{"interactive", "sample-interactive-fgnh9"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				corev1.SchemeGroupVersion.WithKind("Pod"),
			},
			objs: []runtime.Object{
				getSampleInteractive("sample-interactive-fgnh9"),
			},
			wantOut: `Name:        sample-interactive-fgnh9
Namespace:   default
Start Time:  Mon, 01 Jan 2024 00:00:00 +0000
Labels:      kjobctl.x-k8s.io/profile=sample-profile
Status:      Running
Containers:
  sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      /bin/sh
    Environment:
      TASK_NAME:  sample-interactive
    Mounts:
      /sample from sample-volume (rw)
Volumes:
  sample-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>
`,
		},
		"describe all interactive tasks": {
			args:       []string{"interactive"},
			argsFormat: modeTaskArgsFormat,
			mapperKinds: []schema.GroupVersionKind{
				corev1.SchemeGroupVersion.WithKind("Pod"),
			},
			objs: []runtime.Object{
				&corev1.PodList{
					Items: []corev1.Pod{
						*getSampleInteractive("sample-interactive-fgnh9"),
						*getSampleInteractive("sample-interactive-hs2b2"),
					},
				},
			},
			wantOut: `Name:        sample-interactive-fgnh9
Namespace:   default
Start Time:  Mon, 01 Jan 2024 00:00:00 +0000
Labels:      kjobctl.x-k8s.io/profile=sample-profile
Status:      Running
Containers:
  sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      /bin/sh
    Environment:
      TASK_NAME:  sample-interactive
    Mounts:
      /sample from sample-volume (rw)
Volumes:
  sample-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>


Name:        sample-interactive-hs2b2
Namespace:   default
Start Time:  Mon, 01 Jan 2024 00:00:00 +0000
Labels:      kjobctl.x-k8s.io/profile=sample-profile
Status:      Running
Containers:
  sample-container:
    Port:       <none>
    Host Port:  <none>
    Command:
      /bin/sh
    Environment:
      TASK_NAME:  sample-interactive
    Mounts:
      /sample from sample-volume (rw)
Volumes:
  sample-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>
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

				if err := corev1.AddToScheme(scheme); err != nil {
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

func getSampleJob(name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kjobctl.x-k8s.io/profile": "sample-profile",
			},
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To[int32](3),
			Completions: ptr.To[int32](2),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "sample-container",
							Command: []string{"sleep", "15s"},
							Args:    []string{"30s"},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    apiresource.MustParse("1"),
									corev1.ResourceMemory: apiresource.MustParse("200Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: batchv1.JobStatus{
			Active:         0,
			Succeeded:      2,
			Failed:         0,
			StartTime:      ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
			CompletionTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 33, 0, time.UTC)),
		},
	}
}

func getSampleInteractive(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kjobctl.x-k8s.io/profile": "sample-profile",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "sample-container",
					Command: []string{"/bin/sh"},
					Env: []corev1.EnvVar{
						{
							Name:  "TASK_NAME",
							Value: "sample-interactive",
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "sample-volume",
							MountPath: "/sample",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "sample-volume",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: ptr.To(metav1.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
	}
}
