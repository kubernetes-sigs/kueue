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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodOptions_getJobControllerUID(t *testing.T) {
	type fields struct {
		PrintFlags    *genericclioptions.PrintFlags
		AllNamespaces bool
		Namespace     string
		LabelSelector string
		FieldSelector string
		JobArg        string
		Clientset     k8s.Clientset
		PrintObj      printers.ResourcePrinterFunc
		IOStreams     genericiooptions.IOStreams
	}
	type args struct {
		jobName string
	}
	tests := []struct {
		name    string
		jobs    []runtime.Object
		fields  fields
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "get valid controllerUID from a Job",
			jobs: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: "default",
						Labels: map[string]string{
							jobControllerUIDLabel: "test-job-uid",
						},
					},
				},
			},
			args: args{
				jobName: "test-job",
			},
			fields: fields{
				Namespace: "default",
			},
			want:    "test-job-uid",
			wantErr: false,
		}, {
			name: "missing controllerUID label from a Job",
			jobs: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "missing-uid-job",
						Namespace: "default",
						Labels:    nil,
					},
				},
			},
			fields: fields{
				Namespace: "default",
			},
			args: args{
				jobName: "missing-uid-job",
			},
			want:    "",
			wantErr: false,
		}, {
			name: "job not found in the namespace",
			jobs: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: "test-namespace",
						Labels:    nil,
					},
				},
			},
			fields: fields{
				Namespace: "default",
			},
			args: args{
				jobName: "test-job",
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fakeClientset := fake.NewSimpleClientset(tt.jobs...)
			o := &PodOptions{
				PrintFlags:    tt.fields.PrintFlags,
				AllNamespaces: tt.fields.AllNamespaces,
				Namespace:     tt.fields.Namespace,
				LabelSelector: tt.fields.LabelSelector,
				FieldSelector: tt.fields.FieldSelector,
				JobArg:        tt.fields.JobArg,
				Clientset:     fakeClientset,
				PrintObj:      tt.fields.PrintObj,
				IOStreams:     tt.fields.IOStreams,
			}
			got, err := o.getJobControllerUID(context.TODO(), tt.args.jobName)
			if (err != nil) != tt.wantErr {
				t.Errorf("getJobControllerUID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getJobControllerUID() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodOptions_listPodsByControllerUID(t *testing.T) {
	testStartTime := time.Now()

	type fields struct {
		PrintFlags    *genericclioptions.PrintFlags
		AllNamespaces bool
		Namespace     string
		LabelSelector string
		FieldSelector string
		JobArg        string
	}
	type args struct {
		controllerUID string
	}
	tests := []struct {
		name    string
		pods    []runtime.Object
		fields  fields
		args    args
		wantOut []string
		wantErr error
	}{
		{
			name: "list pods by valid controllerUID",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						Labels: map[string]string{
							jobControllerUIDLabel: "valid-controller-uid",
						},
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: "default",
						Labels: map[string]string{
							jobControllerUIDLabel: "valid-controller-uid",
						},
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "invalid-pod",
						Namespace: "default",
						Labels: map[string]string{
							jobControllerUIDLabel: "invalid-controller-uid",
						},
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				},
			},
			fields: fields{
				Namespace: "default",
			},
			args: args{
				controllerUID: "valid-controller-uid",
			},
			wantOut: []string{
				"NAME          STATUS      AGE",
				"valid-pod-1   RUNNING     60m",
				"valid-pod-2   COMPLETED   60m",
				"",
			},
		}, {
			name: "no valid pods matching controllerUID",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: "default",
						Labels: map[string]string{
							jobControllerUIDLabel: "valid-controller-uid",
						},
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				},
			},
			fields: fields{
				Namespace: "default",
			},
			args: args{
				controllerUID: "invalid-controller-uid",
			},
			wantOut: []string{""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClientset := fake.NewSimpleClientset(tt.pods...)
			s, _, out, _ := genericiooptions.NewTestIOStreams()

			o := &PodOptions{
				PrintFlags:    tt.fields.PrintFlags,
				AllNamespaces: tt.fields.AllNamespaces,
				Namespace:     tt.fields.Namespace,
				LabelSelector: tt.fields.LabelSelector,
				FieldSelector: tt.fields.FieldSelector,
				JobArg:        tt.fields.JobArg,
				Clientset:     fakeClientset,
				PrintObj:      printPodTable,
				IOStreams:     s,
			}

			gotErr := o.listPodsByControllerUID(context.TODO(), tt.args.controllerUID)
			if diff := cmp.Diff(tt.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := strings.Split(out.String(), "\n")

			if diff := cmp.Diff(tt.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
