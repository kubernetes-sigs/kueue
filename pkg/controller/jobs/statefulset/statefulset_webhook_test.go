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

package statefulset

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingappwrapper "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/webhook"
	testutil "sigs.k8s.io/kueue/test/util"
)

var (
	admissionGatedByAnnotationsPath = field.NewPath("metadata", "annotations").Key(kueueconstants.AdmissionGatedByAnnotation)
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		initObjs                   []client.Object
		statefulset                *appsv1.StatefulSet
		manageJobsWithoutQueueName bool
		defaultLqExist             bool
		enableIntegrations         []string
		want                       *appsv1.StatefulSet
	}{
		"statefulset without queue with manageJobsWithoutQueueName": {
			enableIntegrations:         []string{"pod"},
			manageJobsWithoutQueueName: true,
			initObjs:                   []client.Object{utiltesting.MakeNamespace("test-ns")},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "test-ns").
				Replicas(10).
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "test-ns").
				Replicas(10).
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"statefulset with queue": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"statefulset managed by another framework": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
		},
		"statefulset with queue and priority class": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"statefulset without replicas": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"default lq is created, job doesn't have queue label": {
			defaultLqExist: true,
			statefulset:    testingstatefulset.MakeStatefulSet("test-pod", "default").Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "default").
				Queue("default").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"default lq is created, job has queue label": {
			defaultLqExist: true,
			statefulset:    testingstatefulset.MakeStatefulSet("test-pod", "").Queue("test-queue").Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"default lq isn't created, job doesn't have queue label": {
			defaultLqExist: false,
			statefulset:    testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
			want:           testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			ctx, _ := utiltesting.ContextWithLog(t)

			builder := utiltesting.NewClientBuilder().WithObjects(tc.initObjs...)
			cli := builder.Build()
			cqCache := schdcache.New(cli)
			queueManager := qcache.NewManagerForUnitTests(cli, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

			w := &Webhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				queues:                     queueManager,
			}

			if err := w.Default(ctx, tc.statefulset); err != nil {
				t.Errorf("failed to set defaults for v1/statefulset: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.statefulset); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		sts          *appsv1.StatefulSet
		wantErr      error
		wantWarns    admission.Warnings
		featureGates map[featuregate.Feature]bool
	}{
		"without queue": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
		},
		"valid queue name": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"statefulset managed by another framework": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test/queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
		},
		"AdmissionGatedBy annotation - single gate": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"AdmissionGatedBy annotation - trailing space": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate ").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"AdmissionGatedBy annotation - space before comma": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate ,example.com/gate2").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"AdmissionGatedBy annotation - space after comma": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate, example.com/gate2").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"AdmissionGatedBy annotation - leading space": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, " example.com/gate").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"AdmissionGatedBy annotation - multiple gates": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/a,not.example.com/b").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"invalid AdmissionGatedBy annotation - not in subdomain/path format": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "this is an invalid value").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(admissionGatedByAnnotationsPath, "this is an invalid value", "must be a domain-prefixed path (such as \"acme.io/foo\")"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"invalid AdmissionGatedBy annotation - duplicate gates": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "duplicates.are/invalid,duplicates.are/invalid").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(admissionGatedByAnnotationsPath, "duplicates.are/invalid,duplicates.are/invalid", "duplicate gate name: duplicates.are/invalid"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"invalid AdmissionGatedBy annotation - gate name too long": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "cannot.be.too.long/"+strings.Repeat("but-this-is-too-long", 20)).
				Obj(),
			wantErr: field.ErrorList{
				field.TooLong(admissionGatedByAnnotationsPath, "", webhook.MaxGateNameLengthForAdmissionGatedBy),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"invalid AdmissionGatedBy annotation - space in path component": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate name").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(admissionGatedByAnnotationsPath, "gate name", testutil.InvalidPathMessage),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"invalid AdmissionGatedBy annotation - space in domain component": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example .com/gate").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(admissionGatedByAnnotationsPath, "example .com", testutil.InvalidRFC1123Message),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"invalid AdmissionGatedBy annotation - multiple gates with one containing space": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "valid.com/gate,invalid gate.com/controller").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(admissionGatedByAnnotationsPath, "invalid gate.com", testutil.InvalidRFC1123Message),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"AdmissionGatedBy annotation with feature gate disabled - valid value": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: false},
		},
		"AdmissionGatedBy annotation with feature gate disabled - invalid value": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "this is an invalid value").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: false},
		},
		"AdmissionGatedBy annotation with feature gate enabled - empty string": {
			sts: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			w := &Webhook{client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateCreate(ctx, tc.sts)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		integrations []string
		objs         []runtime.Object
		oldObj       *appsv1.StatefulSet
		newObj       *appsv1.StatefulSet
		wantErr      error
		featureGates map[featuregate.Feature]bool
	}{
		"no changes": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:        "queue1",
						podconstants.GroupNameLabel: "group1",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:        "queue1",
						podconstants.GroupNameLabel: "group1",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			wantErr: nil,
		},
		"change in queue label": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue-new").
				Obj(),
		},
		"change in queue label (ReadyReplicas > 0)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				ReadyReplicas(1).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue-new").
				ReadyReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"set queue label": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Obj(),
		},
		"set queue label (ReadyReplicas > 0)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				ReadyReplicas(1).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				ReadyReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"delete queue name": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"statefulset managed by another framework": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
		},
		"change in priority class label when suspended": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority2",
					},
				},
			},
		},
		"set in priority class label when replicas ready": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: int32(1),
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority2",
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: int32(1),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: priorityClassNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in priority class label when replicas ready": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority1",
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: int32(1),
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority2",
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: int32(1),
				},
			},
			wantErr: field.ErrorList{}.ToAggregate(),
		},
		"delete in priority class label when replicas ready": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority1",
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: int32(1),
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: int32(1),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: priorityClassNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"set in priority class label when replicas not ready": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority2",
					},
				},
			},
			wantErr: field.ErrorList{}.ToAggregate(),
		},
		"change in priority class label when replicas not ready": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority2",
					},
				},
			},
			wantErr: field.ErrorList{}.ToAggregate(),
		},
		"delete in priority class label when replicas not ready": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: priorityClassNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale down to zero)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
			},
		},
		"change in replicas (scale up from zero)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
		},
		"change in replicas (scale up while the previous scaling operation is still in progress)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale up)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(4).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},

		"change in replicas (scale up without queue-name while the previous scaling operation is still in progress)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
		},
		"change in replicas (scale up without queue-name)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(4).
				Obj(),
		},
		"change in replicas (scale up with AppWrapper ownerReference while the previous scaling operation is still in progress)": {
			integrations: []string{appwrapper.FrameworkName},
			objs: []runtime.Object{
				testingappwrapper.MakeAppWrapper("test-app-wrapper", "test-ns").
					UID("test-app-wrapper").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-app-wrapper", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
				Queue("test-queue").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
		},
		"change in replicas (scale up with AppWrapper ownerReference)": {
			integrations: []string{appwrapper.FrameworkName},
			objs: []runtime.Object{
				testingappwrapper.MakeAppWrapper("test-app-wrapper", "test-ns").
					UID("test-app-wrapper").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-app-wrapper", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
				Queue("test-queue").
				Replicas(4).
				Obj(),
		},
		"change in replicas (scale up with LeaderWorkerSet ownerReference while the previous scaling operation is still in progress)": {
			integrations: []string{leaderworkerset.FrameworkName},
			objs: []runtime.Object{
				testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "test-ns").
					UID("test-lws").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
				Queue("test-queue").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
		},
		"change in replicas (scale up with LeaderWorkerSet ownerReference)": {
			integrations: []string{leaderworkerset.FrameworkName},
			objs: []runtime.Object{
				testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "test-ns").
					UID("test-lws").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
				Queue("test-queue").
				Replicas(4).
				Obj(),
		},
		"attempt to change resources in container": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause:0.1.1",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: podSpecPath.Child("containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"attempt to change resources in init container": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause:0.1.1",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: podSpecPath.Child("containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"scale up from zero blocked by existing workload": {
			objs: []runtime.Object{
				utiltestingapi.MakeWorkload(GetWorkloadName("test-uid", "test-statefulset"), "test-ns").Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-statefulset", "test-ns").
				UID("test-uid").
				Queue("test-queue").
				Replicas(0).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-statefulset", "test-ns").
				UID("test-uid").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
		"scale up from zero allowed when workload does not exist": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				UID("test-sts-uid").
				Queue("test-queue").
				Replicas(0).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				UID("test-sts-uid").
				Queue("test-queue").
				Replicas(3).
				Obj(),
		},
		"reject adding AdmissionGatedBy annotation after StatefulSet creation": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(admissionGatedByAnnotationsPath, "cannot add admission gate after creation"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"allow removing AdmissionGatedBy annotation with single gate": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"allow removing AdmissionGatedBy annotation with multiple gates": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"allow removing one gate from AdmissionGatedBy annotation": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller2").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"reject injecting new gate in AdmissionGatedBy annotation": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller3").
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(admissionGatedByAnnotationsPath, "can only remove gates, not add new ones"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
		"allow reordering gates in AdmissionGatedBy annotation": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "default").
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller2,example.com/controller1").
				Obj(),
			wantErr:      nil,
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.integrations...))

			client := utiltesting.NewClientBuilder(awv1beta2.AddToScheme, leaderworkersetv1.AddToScheme).
				WithRuntimeObjects(tc.objs...).
				Build()

			wh := &Webhook{
				client: client,
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
