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

package raycluster

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestValidateDefault(t *testing.T) {
	testcases := map[string]struct {
		oldJob         *rayv1.RayCluster
		newJob         *rayv1.RayCluster
		manageAll      bool
		defaultLqExist bool
	}{
		"unmanaged": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
		},
		"managed - by config": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(true).
				Obj(),
			manageAll: true,
		},
		"managed - by queue": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
		},
		"default lq is created, job doesn't have queue label": {
			defaultLqExist: true,
			oldJob:         testingrayutil.MakeCluster("test-job", "default").Obj(),
			newJob: testingrayutil.MakeCluster("test-job", "default").
				Queue("default").
				Obj(),
		},
		"default lq is created, job has queue label": {
			defaultLqExist: true,
			oldJob:         testingrayutil.MakeCluster("test-job", "").Queue("test-queue").Obj(),
			newJob: testingrayutil.MakeCluster("test-job", "").
				Queue("test-queue").
				Obj(),
		},
		"default lq isn't created, job doesn't have queue label": {
			defaultLqExist: false,
			oldJob:         testingrayutil.MakeCluster("test-job", "").Obj(),
			newJob: testingrayutil.MakeCluster("test-job", "").
				Obj(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
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

			wh := &RayClusterWebhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageAll,
				queues:                     queueManager,
				cache:                      cqCache,
			}
			result := tc.oldJob.DeepCopy()
			if err := wh.Default(ctx, result); err != nil {
				t.Errorf("unexpected Default() error: %s", err)
			}
			if diff := cmp.Diff(tc.newJob, result); diff != "" {
				t.Errorf("Default() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	bigWorkerGroup := testingrayutil.MakeWorkerGroups(jobframework.MaxPodSets)

	testcases := map[string]struct {
		job          *rayv1.RayCluster
		manageAll    bool
		wantErr      error
		featureGates map[featuregate.Feature]bool
	}{
		"invalid unmanaged": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			job: testingrayutil.MakeCluster("job", "ns").
				Obj(),
			wantErr: nil,
		},
		"invalid managed - has auto scaler": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithEnableAutoscaling(new(true)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "enableInTreeAutoscaling"),
					new(true),
					fmt.Sprintf("a kueue-managed job can use autoscaling only as an elastic job: "+
						"enable the ElasticJobsViaWorkloadSlices feature gate and set the %q: %q annotation",
						workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue),
				),
			}.ToAggregate(),
		},
		"invalid managed - head pod has no containers": {
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{}).
				Obj(),
			wantErr: field.ErrorList{
				field.Required(
					field.NewPath("spec", "headGroupSpec", "template", "spec", "containers"),
					"must have at least one container",
				),
			}.ToAggregate(),
		},
		"invalid MultiKueue elastic autoscaling when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ManagedBy(kueue.MultiKueueControllerName).
				WithEnableAutoscaling(new(true)).
				FirstWorkerGroupReplicas(1, 1, 5).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "enableInTreeAutoscaling"),
					fmt.Sprintf("in-tree autoscaling for a MultiKueue-managed elastic job requires enabling the %s feature gate", features.MultiKueueRayInTreeAutoscaling),
				),
			}.ToAggregate(),
		},
		"valid MultiKueue elastic autoscaling when feature gate is enabled": {
			// The autoscaler runs on the worker and its resizes are written back
			// to the manager, so a range (minReplicas < maxReplicas) is allowed.
			featureGates: map[featuregate.Feature]bool{features.MultiKueueRayInTreeAutoscaling: true, features.WorkloadIdentifierAnnotations: false},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ManagedBy(kueue.MultiKueueControllerName).
				WithEnableAutoscaling(new(true)).
				FirstWorkerGroupReplicas(1, 1, 5).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: nil,
		},
		"valid managed - elastic MultiKueue job without auto scaler": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ManagedBy(kueue.MultiKueueControllerName).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: nil,
		},
		"valid managed - partial preemption opt-in": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: nil,
		},
		"valid managed - partial preemption annotations are ignored with the feature gate disabled": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: false},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "0").
				Obj(),
			wantErr: nil,
		},
		"valid managed - partial preemption max reclaimed count is ignored without opt-in": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "0").
				Obj(),
			wantErr: nil,
		},
		"invalid managed - partial preemption on a MultiKueue-managed job": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				ManagedBy(kueue.MultiKueueControllerName).
				Obj(),
			wantErr: field.ErrorList{field.Forbidden(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation),
				"partial preemption is not supported for a MultiKueue-managed job")}.ToAggregate(),
		},
		"invalid managed - partial preemption on a non-elastic job": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation),
					"true",
					fmt.Sprintf(
						"requires an elastic job: enable the ElasticJobsViaWorkloadSlices feature gate and set the %q: %q annotation",
						workloadslicing.EnabledAnnotationKey,
						workloadslicing.EnabledAnnotationValue,
					),
				),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption with two worker groups": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: func() *rayv1.RayCluster {
				c := testingrayutil.MakeCluster("job", "ns").Queue("queue").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
					SchedulingGate(kueue.ElasticJobSchedulingGate).
					Obj()
				second := *c.Spec.WorkerGroupSpecs[0].DeepCopy()
				second.GroupName = "workers-group-1"
				c.Spec.WorkerGroupSpecs = append(c.Spec.WorkerGroupSpecs, second)
				return c
			}(),
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "workerGroupSpecs"), 2, "partial preemption requires exactly one worker group")}.ToAggregate(),
		},
		"invalid managed - partial preemption with numOfHosts above one": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				WithNumOfHosts("workers-group-0", 2).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("numOfHosts"), int32(2), "partial preemption requires numOfHosts to be at most 1"),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption with partial scale-up strategy": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.ElasticJobScaleUpStrategyAnnotationKey, kueueconstants.ElasticJobScaleUpStrategyPartial).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation),
					"true",
					fmt.Sprintf("is mutually exclusive with the %q: %q annotation: partial scale-up sets the worker PodSet minCount to its count, while partial preemption sets it to minReplicas",
						kueueconstants.ElasticJobScaleUpStrategyAnnotationKey, kueueconstants.ElasticJobScaleUpStrategyPartial),
				),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption max reclaimed count is not positive": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "0").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation), "0", "must be a positive 32-bit integer"),
			}.ToAggregate(),
		},
		"valid managed - partial preemption max reclaimed count": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "2").
				Obj(),
			wantErr: nil,
		},
		"invalid managed - partial preemption with in-tree autoscaling": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				WithEnableAutoscaling(new(true)).
				Obj(),
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "enableInTreeAutoscaling"), new(true), "partial preemption is not supported with in-tree autoscaling")}.ToAggregate(),
		},
		"invalid managed - too many worker groups": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithWorkerGroups(bigWorkerGroup...).
				Obj(),
			wantErr: field.ErrorList{
				field.TooMany(field.NewPath("spec", "workerGroupSpecs"), jobframework.MaxPodSets+1, jobframework.MaxPodSets),
			}.ToAggregate(),
		},
		"worker group uses head name": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithWorkerGroups(rayv1.WorkerGroupSpec{
					GroupName: headGroupPodSetName,
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)),
			}.ToAggregate(),
		},
		"valid topology request": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg2",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{GroupName: "wg3"},
				).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid topology request": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
									kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec.headGroupSpec.template.metadata.annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
				field.Invalid(
					field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid slice topology request - slice size larger than number of podsets": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
								kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
								kueue.PodSetSliceSizeAnnotation:             "2",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  new(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
									kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
									kueue.PodSetSliceSizeAnnotation:             "10",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg2",
						Replicas:  new(int32(10)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
									kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
									kueue.PodSetSliceSizeAnnotation:             "20",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.headGroupSpec.template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "2", "must not be greater than pod set count 1"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "10", "must not be greater than pod set count 5"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[1].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "20", "must not be greater than pod set count 10"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"valid PodSet grouping request": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetGroupName:                  "groupname",
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "groupname",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
				).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid PodSet grouping request - groups of size other than 2": {
			job: testingrayutil.MakeCluster("rayjob", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetGroupName:                  "1podset",
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  new(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "3podsets",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg2",
						Replicas:  new(int32(10)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "3podsets",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg3",
						Replicas:  new(int32(10)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "3podsets",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.headGroupSpec.template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "1podset", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "3podsets", "can only define groups of exactly 2 pod sets, got: 3 pod set(s)"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[1].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "3podsets", "can only define groups of exactly 2 pod sets, got: 3 pod set(s)"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[2].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "3podsets", "can only define groups of exactly 2 pod sets, got: 3 pod set(s)"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid PodSet grouping request - no leader in group": {
			job: testingrayutil.MakeCluster("rayjob", "ns").Queue("queue").
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  new(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "groupname",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg2",
						Replicas:  new(int32(10)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "groupname",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "groupname", "can only define groups where at least one pod set has only 1 replica, got: 5 replica(s) and 10 replica(s) in the group"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[1].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "groupname", "can only define groups where at least one pod set has only 1 replica, got: 5 replica(s) and 10 replica(s) in the group"),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid PodSet grouping request - required topology does not match": {
			job: testingrayutil.MakeCluster("rayjob", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetGroupName:                  "groupname",
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  new(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "groupname",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec.headGroupSpec.template.metadata.annotations"),
					field.OmitValueType{},
					"must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.workerGroupSpecs[0].template.metadata.annotations' in group 'groupname'",
				),
				field.Invalid(
					field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations"),
					field.OmitValueType{},
					"must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.headGroupSpec.template.metadata.annotations' in group 'groupname'",
				),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid PodSet grouping request - preferred topology does not match": {
			job: testingrayutil.MakeCluster("rayjob", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetGroupName:                   "groupname",
								kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  new(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                   "groupname",
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec.headGroupSpec.template.metadata.annotations"),
					field.OmitValueType{},
					"must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.workerGroupSpecs[0].template.metadata.annotations' in group 'groupname'",
				),
				field.Invalid(
					field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations"),
					field.OmitValueType{},
					"must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.headGroupSpec.template.metadata.annotations' in group 'groupname'",
				),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"invalid PodSet grouping request - different topology annotations within group": {
			job: testingrayutil.MakeCluster("rayjob", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetGroupName:                   "groupname",
								kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-head"}}},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  new(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetGroupName:                  "groupname",
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec.headGroupSpec.template.metadata.annotations"),
					field.OmitValueType{},
					"must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.workerGroupSpecs[0].template.metadata.annotations' in group 'groupname'",
				),
				field.Invalid(
					field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations"),
					field.OmitValueType{},
					"must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.headGroupSpec.template.metadata.annotations' in group 'groupname'",
				),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"valid with prebuilt workload annotation, WorkloadIdentifierAnnotations enabled": {
			job: testingrayutil.MakeCluster("raycluster", "ns").
				Queue("queue").
				PrebuiltWorkloadAnnotation("wl1").
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, result := wh.ValidateCreate(ctx, tc.job)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateCreate() errors mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(admission.Warnings(nil), warns); diff != "" {
				t.Errorf("ValidateCreate() warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	validPartialPreemptionCluster := func() *testingrayutil.ClusterWrapper {
		return testingrayutil.MakeCluster("job", "ns").Queue("queue").
			Suspend(false).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true")
	}
	testcases := map[string]struct {
		oldJob               *rayv1.RayCluster
		newJob               *rayv1.RayCluster
		manageAll            bool
		defaultLqExist       bool
		featureGates         map[featuregate.Feature]bool
		wantErr              error
		maxTimeoutOnWorkload *metav1.Duration
	}{
		"invalid unmanaged": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Obj(),
			wantErr: nil,
		},
		"adding a queue name to an unmanaged cluster with an invalid partial preemption config is rejected": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(true).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "a"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(true).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "a"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "workerGroupSpecs"), 2, "partial preemption requires exactly one worker group"),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption annotation should not be added while unsuspended": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation), "true", apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption annotation should not be added while suspended": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(true).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "a"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(true).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "a"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation), "true", apivalidation.FieldImmutableErrorMsg),
				field.Invalid(field.NewPath("spec", "workerGroupSpecs"), 2, "partial preemption requires exactly one worker group"),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption annotation should not be removed": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation), "", apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"invalid managed - partial preemption worker minReplicas should not change": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).FirstWorkerGroupReplicas(4, 1, 8).Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).FirstWorkerGroupReplicas(4, 2, 8).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("minReplicas"), int32(2), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"valid managed - worker minReplicas can change without partial preemption": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				FirstWorkerGroupReplicas(4, 1, 8).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				FirstWorkerGroupReplicas(4, 2, 8).
				Obj(),
			wantErr: nil,
		},
		"valid managed - partial preemption worker replicas can be lowered": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).FirstWorkerGroupReplicas(4, 1, 8).Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).FirstWorkerGroupReplicas(2, 1, 8).Obj(),
			wantErr: nil,
		},
		"invalid managed - valid partial preemption cluster cannot add a worker group": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob:       validPartialPreemptionCluster().SchedulingGate(kueue.ElasticJobSchedulingGate).Obj(),
			newJob: validPartialPreemptionCluster().
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "workers-group-0"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "workerGroupSpecs"), 2, "partial preemption requires exactly one worker group"),
			}.ToAggregate(),
		},
		"invalid managed - valid partial preemption cluster cannot set numOfHosts above 1": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob:       validPartialPreemptionCluster().SchedulingGate(kueue.ElasticJobSchedulingGate).Obj(),
			newJob: validPartialPreemptionCluster().
				WithNumOfHosts("workers-group-0", 2).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("numOfHosts"), int32(2), "partial preemption requires numOfHosts to be at most 1"),
			}.ToAggregate(),
		},
		"invalid managed - valid partial preemption cluster cannot enable in-tree autoscaling": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob:       validPartialPreemptionCluster().SchedulingGate(kueue.ElasticJobSchedulingGate).Obj(),
			newJob: validPartialPreemptionCluster().
				WithEnableAutoscaling(new(true)).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "enableInTreeAutoscaling"), new(true), "partial preemption is not supported with in-tree autoscaling"),
			}.ToAggregate(),
		},
		"invalid managed - valid partial preemption cluster cannot set managedBy to MultiKueue": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob:       validPartialPreemptionCluster().SchedulingGate(kueue.ElasticJobSchedulingGate).Obj(),
			newJob: validPartialPreemptionCluster().
				ManagedBy(kueue.MultiKueueControllerName).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionAnnotation), "partial preemption is not supported for a MultiKueue-managed job"),
			}.ToAggregate(),
		},
		"invalid managed - valid partial preemption cluster cannot set an invalid max reclaimed count": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob:       validPartialPreemptionCluster().SchedulingGate(kueue.ElasticJobSchedulingGate).Obj(),
			newJob: validPartialPreemptionCluster().
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "0").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation), "0", "must be a positive 32-bit integer"),
			}.ToAggregate(),
		},
		"valid managed - valid partial preemption cluster can be suspended and scaled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: validPartialPreemptionCluster().
				FirstWorkerGroupReplicas(4, 1, 8).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			newJob: validPartialPreemptionCluster().
				Suspend(true).
				FirstWorkerGroupReplicas(2, 1, 8).
				SetAnnotation("test-annotation", "test-value").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			wantErr: nil,
		},
		"valid managed - invalid partial preemption cluster stays updatable": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "0").
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "a"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				SetAnnotation(kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation, "0").
				WithWorkerGroups(rayv1.WorkerGroupSpec{GroupName: "a"}, rayv1.WorkerGroupSpec{GroupName: "b"}).
				SchedulingGate(kueue.ElasticJobSchedulingGate).
				Suspend(true).
				Obj(),
			wantErr: nil,
		},
		"valid managed - partial preemption cluster can be suspended with the feature gates disabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false, features.PartialPreemption: false},
			oldJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(false).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(4, 1, 8).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				Suspend(true).
				SetAnnotation(kueueconstants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(4, 1, 8).
				Obj(),
			wantErr: nil,
		},
		"queue name unchanged while unsuspended": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Label("test-label", "test-value").
				Obj(),
			wantErr: nil,
		},
		"invalid managed - queue name should not change while unsuspended": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue2").
				Suspend(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), kueue.LocalQueueName("queue2"), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"managed - queue name can change while suspended": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue2").
				Suspend(true).
				Obj(),
			wantErr: nil,
		},
		"queue name removal is rejected when the job is unsuspended and ValidateRayAndSparkJobUpdates is enabled": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), kueue.LocalQueueName(""), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"queue name removal is allowed when the job is unsuspended and ValidateRayAndSparkJobUpdates is disabled": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: false},
			wantErr:      nil,
		},
		"queue name removal is rejected when the job is suspended in a namespace with a default queue and ValidateRayAndSparkJobUpdates is enabled": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue(string(constants.DefaultLocalQueueName)).
				Suspend(true).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(true).
				Obj(),
			defaultLqExist: true,
			featureGates:   map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), "", "queue-name must not be empty in namespace with default queue"),
			}.ToAggregate(),
		},
		"queue name removal is allowed when the job is suspended in a namespace with a default queue and ValidateRayAndSparkJobUpdates is disabled": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue(string(constants.DefaultLocalQueueName)).
				Suspend(true).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(true).
				Obj(),
			defaultLqExist: true,
			featureGates:   map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: false},
			wantErr:        nil,
		},
		"queue name removal is allowed when the job is suspended in a namespace without a default queue and ValidateRayAndSparkJobUpdates is enabled": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(true).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr:      nil,
		},
		"prebuilt workload name change is not validated when the job is unmanaged and ValidateRayAndSparkJobUpdates is enabled": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				PrebuiltWorkloadLabel("wl1").
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				PrebuiltWorkloadLabel("wl2").
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr:      nil,
		},
		"priorityClassName is mutable": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				WorkloadPriorityClass("test-1").
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				WorkloadPriorityClass("test-2").
				Obj(),
		},
		"unchanged wait-for-pods-ready annotation exceeding maxTimeoutOnWorkload is not re-validated on update": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				SetAnnotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":3600}`).Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				SetAnnotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":3600}`).Obj(),
			wantErr:              nil,
			maxTimeoutOnWorkload: &metav1.Duration{Duration: 60 * time.Second},
			featureGates:         map[featuregate.Feature]bool{features.WorkloadLevelWaitForPodsReady: true},
		},
		"changed wait-for-pods-ready annotation exceeding maxTimeoutOnWorkload is rejected on update": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				SetAnnotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":30}`).Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				SetAnnotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":3600}`).Obj(),
			maxTimeoutOnWorkload: &metav1.Duration{Duration: 60 * time.Second},
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata", "annotations").Key(constants.WaitForPodsReadyAnnotation),
					float64(3600),
					"timeoutSeconds must be less than or equal to 60 seconds",
				),
			}.ToAggregate(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadLevelWaitForPodsReady: true},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			cli := utiltesting.NewClientBuilder().Build()
			cqCache := schdcache.New(cli)
			queueManager := qcache.NewManagerForUnitTests(cli, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue(
					string(constants.DefaultLocalQueueName), "ns").ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("Failed to add the default LocalQueue: %v", err)
				}
			}
			wh := &RayClusterWebhook{
				maxTimeoutOnWorkload:       tc.maxTimeoutOnWorkload,
				manageJobsWithoutQueueName: tc.manageAll,
				queues:                     queueManager,
				cache:                      cqCache,
			}
			warnings, result := wh.ValidateUpdate(ctx, tc.oldJob, tc.newJob)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateUpdate() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(admission.Warnings(nil), warnings); diff != "" {
				t.Errorf("ValidateUpdate() warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
