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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	fakediscovery "k8s.io/client-go/discovery/fake"
	restfake "k8s.io/client-go/rest/fake"
	kubetesting "k8s.io/client-go/testing"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestWorkloadCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns               string
		apiResourceLists []*metav1.APIResourceList
		pendingWorkloads []visibility.PendingWorkload
		objs             []runtime.Object
		args             []string
		mapperKinds      []schema.GroupVersionKind
		job              []runtime.Object
		wantOut          string
		wantOutErr       string
		wantErr          error
	}{
		"should print workload list with namespace filter": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", "ns1").
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", "ns2").
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with localqueue filter": {
			args: []string{"--localqueue", "lq1"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with localqueue filter (short flag)": {
			args: []string{"-q", "lq1"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with clusterqueue filter": {
			args: []string{"--clusterqueue", "cq1"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with clusterqueue filter (short flag)": {
			args: []string{"-c", "cq1"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with all status flag": {
			args: []string{"--status", "all"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl3", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j3", "test-uid").
					Queue("lq3").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq3").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl4", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j4", "test-uid").
					Queue("lq4").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq4").Obj()).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl5", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j5", "test-uid").
					Queue("lq5").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq5").Obj()).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS     POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                    60m
wl2               j2         lq2          cq2            ADMITTED                       60m         120m
wl3               j3         lq3          cq3            PENDING                                    120m
wl4               j4         lq4          cq4            FINISHED                       60m         3h
wl5               j5         lq5          cq5            ADMITTED                       120m        3h
`,
		},
		"should print workload list with only admitted and finished status flags": {
			args: []string{"--status", "admitted", "--status", "finished"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl3", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j3", "test-uid").
					Queue("lq3").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq3").Obj()).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS     POSITION IN QUEUE   EXEC TIME   AGE
wl2               j2         lq2          cq2            ADMITTED                       60m         120m
wl3               j3         lq3          cq3            FINISHED                       60m         3h
`,
		},
		"should print workload list with only pending filter": {
			args: []string{"--status", "pending"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:   kueue.WorkloadFinished,
							Status: metav1.ConditionTrue,
						},
					}...).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with only quotareserved filter": {
			args: []string{"--status", "quotareserved"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:   kueue.WorkloadQuotaReserved,
							Status: metav1.ConditionTrue,
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:   kueue.WorkloadQuotaReserved,
							Status: metav1.ConditionFalse,
						},
					}...).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS          POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            QUOTARESERVED                                   60m
`,
		},
		"should print workload list with only admitted filter": {
			args: []string{"--status", "admitted"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
					}...).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS     POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            ADMITTED                       60m         60m
`,
		},
		"should print workload list with only finished status filter": {
			args: []string{"--status", "finished"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Truncate(time.Second)),
						},
					}...).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Conditions([]metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(testStartTime.Truncate(time.Second)),
						},
					}...).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS     POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            FINISHED                       60m         60m
`,
		},
		"should print workload list with label selector filter": {
			args: []string{"--selector", "key=value1"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					Label("key", "value1").
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Label("key", "value2").
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with label selector filter (short flag)": {
			args: []string{"-l", "key=value1"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					Label("key", "value1").
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Label("key", "value2").
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING                                   60m
`,
		},
		"should print workload list with Job types": {
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "",
						},
					},
				},
				{
					GroupVersion: "ray.io/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "rayjob",
							Kind:         "RayJob",
							Group:        "ray.io",
						},
					},
				}, {
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "pytorchjob",
							Kind:         "PyTorchJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl3", metav1.NamespaceDefault).
					OwnerReference(kftraining.SchemeGroupVersion.WithKind(kftraining.PyTorchJobKind), "j3", "test-uid").
					Queue("lq3").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq3").Obj()).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE                  JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1    job                       j1         lq1          cq1            PENDING                                   60m
wl2    rayjob.ray.io             j2         lq2          cq2            PENDING                                   120m
wl3    pytorchjob.kubeflow....   j3         lq3          cq3            PENDING                                   3h
`,
		},
		"should print workload list with resource filter": {
			args: []string{"--for", "job.batch/job-test"},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "batch",
						},
					},
				},
			},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					Label(constants.JobUIDLabel, "job-test-uid").
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job-test", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(kftraining.SchemeGroupVersion.WithKind(kftraining.PyTorchJobKind), "job-test", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			mapperKinds: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			job: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "job-test",
						Namespace: "default",
						UID:       types.UID("job-test-uid"),
					},
				},
			},
			wantOut: `NAME   JOB TYPE    JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1    job.batch   job-test   lq1          cq1            PENDING                                   120m
`,
		},
		"should print workload list with resource filter and composable jobs": {
			args: []string{"--for", "pod/pod-test-1"},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "batch",
						},
					},
				},
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "pod",
							Kind:         "Pod",
						},
					},
				},
			},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					Label(constants.JobUIDLabel, "job-test-uid").
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job-test", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod-test-1", "pod-test-uid-1").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			mapperKinds: []schema.GroupVersionKind{
				corev1.SchemeGroupVersion.WithKind("Pod"),
			},
			job: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-test",
						Namespace: "default",
						UID:       types.UID("pod-test-uid-1"),
					},
				},
			},
			wantOut: `NAME   JOB TYPE   JOB NAME     LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl2    pod        pod-test-1   lq2          cq2            PENDING                                   3h
`,
		},
		"should print workload list with custom resource filter": {
			args: []string{"--for", "rayjob/job-test"},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "ray.io/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "rayjob",
							Kind:         "RayJob",
							Group:        "ray.io",
						},
					},
				},
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "pytorchjob",
							Kind:         "PyTorchJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					Label(constants.JobUIDLabel, "job-test-uid-1").
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "job-test", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					Label(constants.JobUIDLabel, "job-test-uid-2").
					OwnerReference(kftraining.SchemeGroupVersion.WithKind(kftraining.PyTorchJobKind), "job-test", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			mapperKinds: []schema.GroupVersionKind{
				rayv1.GroupVersion.WithKind("RayJob"),
			},
			job: []runtime.Object{
				&rayv1.RayJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "job-test",
						Namespace: "default",
						UID:       types.UID("job-test-uid-1"),
					},
				},
			},
			wantOut: `NAME   JOB TYPE        JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1    rayjob.ray.io   job-test   lq1          cq1            PENDING                                   120m
`,
		},
		"should print workload list with full resource filter": {
			args: []string{"--for", "rayjob.ray.io/job-test"},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "ray.io/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "rayjob",
							Kind:         "RayJob",
							Group:        "ray.io",
						},
					},
				},
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "pytorchjob",
							Kind:         "PyTorchJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					Label(constants.JobUIDLabel, "job-test-uid-1").
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "job-test", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					Label(constants.JobUIDLabel, "job-test-uid-2").
					OwnerReference(kftraining.SchemeGroupVersion.WithKind(kftraining.PyTorchJobKind), "job-test", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
						Reason: "Pending",
					}).
					Creation(testStartTime.Add(-3 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			mapperKinds: []schema.GroupVersionKind{
				rayv1.GroupVersion.WithKind("RayJob"),
			},
			job: []runtime.Object{
				&rayv1.RayJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "job-test",
						Namespace: "default",
						UID:       types.UID("job-test-uid-1"),
					},
				},
			},
			wantOut: `NAME   JOB TYPE        JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1    rayjob.ray.io   job-test   lq1          cq1            PENDING                                   120m
`,
		},
		"should print workload list with position in queue": {
			pendingWorkloads: []visibility.PendingWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "wl1",
						Namespace: metav1.NamespaceDefault,
					},
					Priority:               10,
					LocalQueueName:         "lq1",
					PositionInClusterQueue: 11,
					PositionInLocalQueue:   12,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "wl2",
						Namespace: metav1.NamespaceDefault,
					},
					Priority:               20,
					LocalQueueName:         "lq2",
					PositionInClusterQueue: 21,
					PositionInLocalQueue:   22,
				},
			},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1               j1         lq1          cq1            PENDING   12                              60m
wl2               j2         lq2          cq2            PENDING   22                              120m
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

			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(clientset)
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			if len(tc.mapperKinds) != 0 {
				mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
				for _, k := range tc.mapperKinds {
					mapper.Add(k, meta.RESTScopeNamespace)
				}
				tcg.WithRESTMapper(mapper)
			}

			if len(tc.job) != 0 {
				scheme := runtime.NewScheme()
				if err := rayv1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}
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
						Body:       io.NopCloser(strings.NewReader(runtime.EncodeOrDie(codec, tc.job[0]))),
					},
				})
			}

			// `SimpleClientset` not allow to add `PendingWorkloadsSummary` objects,
			// because of `PendingWorkload` resources not implement `runtime.Object`.
			// Default `Reaction` handle all verbs and resources, so need to add on
			// head of chain.
			clientset.PrependReactor("get", "clusterqueues", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				obj := &visibility.PendingWorkloadsSummary{Items: tc.pendingWorkloads}
				return true, obj, err
			})
			clientset.Discovery().(*fakediscovery.FakeDiscovery).Resources = tc.apiResourceLists

			fc := testingclock.NewFakeClock(testStartTime)
			cmd := NewWorkloadCmd(tcg, streams, fc)
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
