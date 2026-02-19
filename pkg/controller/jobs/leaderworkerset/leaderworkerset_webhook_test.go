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

package leaderworkerset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		lws                        *leaderworkersetv1.LeaderWorkerSet
		manageJobsWithoutQueueName bool
		defaultLqExist             bool
		enableIntegrations         []string
		want                       *leaderworkersetv1.LeaderWorkerSet
		wantErr                    error
	}{
		"LeaderWorkerSet with WorkloadPriorityClass": {
			defaultLqExist: true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkloadPriorityClass("high-priority").
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("default").
				WorkloadPriorityClass("high-priority").
				LeaderTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "high-priority").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				LeaderTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "high-priority").
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecAnnotation(kueue.PodIndexOffsetAnnotation, "1").
				Obj(),
		},
		"default lq is created, job doesn't have queue label": {
			defaultLqExist: true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("default").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				LeaderTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecAnnotation(kueue.PodIndexOffsetAnnotation, "1").
				Obj(),
		},
		"default lq is created, job has queue label": {
			defaultLqExist: true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				LeaderTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecAnnotation(kueue.PodIndexOffsetAnnotation, "1").
				Obj(),
		},
		"default lq isn't created, job doesn't have queue label": {
			defaultLqExist: false,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
		},
		"worker-only LWS (no leader template), no offset annotation": {
			defaultLqExist: true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				Queue("default").
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
		},
		"LWS with PodSetGroupName set, no offset annotation": {
			defaultLqExist: true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkerTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("default").
				WorkerTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				LeaderTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			ctx, _ := utiltesting.ContextWithLog(t)

			builder := utiltesting.NewClientBuilder()
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

			err := w.Default(ctx, tc.lws)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want, +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, tc.lws); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		integrations []string
		lws          *leaderworkersetv1.LeaderWorkerSet
		wantErr      error
		wantWarns    admission.Warnings
	}{
		"without queue": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
		},
		"valid queue name": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"valid topology request": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
		},
		"invalid topology request": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations",
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.workerTemplate.metadata.annotations",
				},
			}.ToAggregate(),
		},
		"invalid slice topology request - slice size larger than number of podsets": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
							kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueue.PodSetSliceSizeAnnotation:             "2",
						},
					},
				}).
				Size(4).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
							kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueue.PodSetSliceSizeAnnotation:             "20",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "2", "must not be greater than pod set count 1"),
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "20", "must not be greater than pod set count 3"),
			}.ToAggregate(),
		},
		"invalid slice topology request - slice size provided without slice topology": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetSliceSizeAnnotation: "1",
						},
					},
				}).
				Size(4).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetSliceSizeAnnotation: "1",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "may not be set when 'kueue.x-k8s.io/podset-slice-required-topology' is not specified"),
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "may not set when 'kueue.x-k8s.io/podset-slice-required-topology' is not specified"),
			}.ToAggregate(),
		},
		"invalid slice topology request - slice topology requested without slice size": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Size(4).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Required(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "must be set when 'kueue.x-k8s.io/podset-slice-required-topology' is specified"),
				field.Required(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "must be set when 'kueue.x-k8s.io/podset-slice-required-topology' is specified"),
			}.ToAggregate(),
		},
		"invalid slice topology request - grouping requested together with slicing": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                       "groupname",
							kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
							kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueue.PodSetSliceSizeAnnotation:             "1",
						},
					},
				}).
				Size(4).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                       "groupname",
							kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
							kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueue.PodSetSliceSizeAnnotation:             "1",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "may not be set when 'kueue.x-k8s.io/podset-slice-size' is specified"),
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "may not be set when 'kueue.x-k8s.io/podset-slice-required-topology' is specified"),
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "may not be set when 'kueue.x-k8s.io/podset-slice-size' is specified"),
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "may not be set when 'kueue.x-k8s.io/podset-slice-required-topology' is specified"),
			}.ToAggregate(),
		},
		"valid PodSet group name request": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: nil,
		},
		"invalid PodSet group name request - value is a number": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "1234",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "1234",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "1234", "must not be a number"),
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "1234", "must not be a number"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - group specified only in leader": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]"), "groupname", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - group specified only in worker": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]"), "groupname", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - group name does not match": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname1",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname2",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]"), "groupname1", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]"), "groupname2", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - required topology request does not match": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.leaderWorkerTemplate.workerTemplate.metadata.annotations' in group 'groupname'"),
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations' in group 'groupname'"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - preferred topology request does not match": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                   "groupname",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                   "groupname",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.leaderWorkerTemplate.workerTemplate.metadata.annotations' in group 'groupname'"),
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations' in group 'groupname'"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - different topology annotations within group": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                  "groupname",
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                   "groupname",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.leaderWorkerTemplate.workerTemplate.metadata.annotations' in group 'groupname'"),
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations' in group 'groupname'"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - neither preferred nor required topology is requested": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName: "groupname",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName: "groupname",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "may not be set when neither 'kueue.x-k8s.io/podset-preferred-topology' nor 'kueue.x-k8s.io/podset-required-topology' is specified"),
				field.Forbidden(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "may not be set when neither 'kueue.x-k8s.io/podset-preferred-topology' nor 'kueue.x-k8s.io/podset-required-topology' is specified"),
			}.ToAggregate(),
		},
		"invalid PodSet grouping request - grouping requested without leader template": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetGroupName:                   "groupname",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.leaderWorkerTemplate.workerTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]"), "groupname", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			for _, integration := range tc.integrations {
				jobframework.EnableIntegrationsForTest(t, integration)
			}
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			w := &Webhook{client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateCreate(ctx, tc.lws)
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
		oldObj       *leaderworkersetv1.LeaderWorkerSet
		newObj       *leaderworkersetv1.LeaderWorkerSet
		wantErr      error
	}{
		"no changes": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"change queue name": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("new-test-queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"delete queue name": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change priority class when suspended": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				Obj(),
		},
		"set priority class when replicas ready": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				ReadyReplicas(int32(1)).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				ReadyReplicas(int32(1)).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/priority-class]",
				},
			}.ToAggregate(),
		},
		"change priority class when replicas ready": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				ReadyReplicas(int32(1)).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				ReadyReplicas(int32(1)).
				Obj(),
			wantErr: field.ErrorList{}.ToAggregate(),
		},
		"delete priority class when replicas ready": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				ReadyReplicas(int32(1)).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				ReadyReplicas(int32(1)).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/priority-class]",
				},
			}.ToAggregate(),
		},
		"set priority class when replicas not ready": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			wantErr: field.ErrorList{}.ToAggregate(),
		},
		"change priority class when replicas not ready": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				ReadyReplicas(int32(1)).
				Obj(),
			wantErr: field.ErrorList{}.ToAggregate(),
		},
		"delete priority class when replicas not ready": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				ReadyReplicas(int32(1)).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/priority-class]",
				},
			}.ToAggregate(),
		},
		"change image": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
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
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"change resources in container": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
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
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause",
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
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: leaderTemplatePath.Child("spec", "containers").Index(0).Child("resources", "requests").String(),
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: workerTemplatePath.Child("spec", "containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"change resources in init containers": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
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
								Name:  "ic",
								Image: "pause:0.1.1",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:  "ic",
								Image: "pause",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: leaderTemplatePath.Child("spec", "initContainers").Index(0).Child("resources", "requests").String(),
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: workerTemplatePath.Child("spec", "initContainers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"set valid topology request": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkerTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Queue("test-queue").
				Obj(),
		},
		"set invalid topology request": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkerTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Queue("test-queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations",
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.workerTemplate.metadata.annotations",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for _, integration := range tc.integrations {
				jobframework.EnableIntegrationsForTest(t, integration)
			}
			wh := &Webhook{}

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
