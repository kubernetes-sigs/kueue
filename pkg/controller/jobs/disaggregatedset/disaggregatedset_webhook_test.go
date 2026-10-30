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

package disaggregatedset

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	queueNameLabelPath     = field.NewPath("metadata", "labels").Key(constants.QueueLabel)
	priorityClassLabelPath = field.NewPath("metadata", "labels").Key(constants.WorkloadPriorityClassLabel)
	maxSurgePath           = rolesPath.Index(0).Child("spec", "rolloutStrategy", "rollingUpdateConfiguration", "maxSurge")
	role0LWTPath           = rolesPath.Index(0).Child("spec", "leaderWorkerTemplate")
	role0LeaderMetaPath    = role0LWTPath.Child("leaderTemplate", "metadata")
	role0WorkerMetaPath    = role0LWTPath.Child("workerTemplate", "metadata")
	role1WorkerMetaPath    = rolesPath.Index(1).Child("spec", "leaderWorkerTemplate", "workerTemplate", "metadata")
	role0AnnotationsPath   = rolesPath.Index(0).Child("metadata", "annotations")
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		oldDS                      *disaggregatedsetv1.DisaggregatedSet
		ds                         *disaggregatedsetv1.DisaggregatedSet
		manageJobsWithoutQueueName bool
		defaultLqExist             bool
		defaultWPCExist            bool
		want                       *disaggregatedsetv1.DisaggregatedSet
	}{
		"unmanaged DisaggregatedSet is not changed": {
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill"), testingds.MakeRole("decode").WithLeader()).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill"), testingds.MakeRole("decode").WithLeader()).
				Obj(),
		},
		"managed DisaggregatedSet gets only the constant annotations": {
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill"), testingds.MakeRole("decode").WithLeader().Size(2)).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(
					testingds.MakeRole("prefill").
						PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
						PodGroupServingAnnotation(),
					testingds.MakeRole("decode").WithLeader().Size(2).
						PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
						PodGroupServingAnnotation(),
				).
				Obj(),
		},
		"manageJobsWithoutQueueName manages a DisaggregatedSet without a queue name": {
			manageJobsWithoutQueueName: true,
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill")).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"default LocalQueue is applied on create": {
			defaultLqExist: true,
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill")).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("default").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"default LocalQueue is applied on update while no role is Ready": {
			defaultLqExist: true,
			oldDS: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill")).
				RoleStatus("prefill", 0).
				Obj(),
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill")).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("default").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"default LocalQueue is not applied on update while a role is Ready": {
			defaultLqExist: true,
			oldDS: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill"), testingds.MakeRole("decode")).
				RoleStatus("prefill", 0).
				RoleStatus("decode", 1).
				Obj(),
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill").Replicas(0), testingds.MakeRole("decode").Replicas(0)).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Roles(testingds.MakeRole("prefill").Replicas(0), testingds.MakeRole("decode").Replicas(0)).
				Obj(),
		},
		"default WorkloadPriorityClass is applied on create": {
			defaultWPCExist: true,
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill")).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				WorkloadPriorityClass(constants.DefaultWorkloadPriorityClassName).
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"default WorkloadPriorityClass is not applied on update while a role is Ready": {
			defaultWPCExist: true,
			oldDS: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				RoleStatus("prefill", 1).
				Obj(),
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"re-applying a manifest without the stamped annotations restores them": {
			oldDS: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WithLeader().
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				RoleStatus("prefill", 1).
				Obj(),
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WithLeader()).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WithLeader().
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"pod-index-offset is never stamped": {
			ds: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).
					WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block")).
				Obj(),
			want: testingds.MakeDisaggregatedSet("ds", "default").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).
					WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			integrationManager := newTestIntegrationManager(t)
			ctx, _ := utiltesting.ContextWithLog(t)
			if tc.oldDS != nil {
				ctx = utiltesting.ContextWithUpdateRequest(ctx, t, tc.oldDS)
			}

			builder := utiltesting.NewClientBuilder()
			if tc.defaultWPCExist {
				builder = builder.WithObjects(utiltestingapi.MakeWorkloadPriorityClass(constants.DefaultWorkloadPriorityClassName).PriorityValue(100).Obj())
			}
			cli := builder.Build()
			queueManager := qcache.NewManagerForUnitTests(cli, schdcache.New(cli))
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("Failed to create default local queue: %v", err)
				}
			}

			w := &Webhook{
				integrationManager:         integrationManager,
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				queues:                     queueManager,
			}
			if err := w.Default(ctx, tc.ds); err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, tc.ds); diff != "" {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		ds           *disaggregatedsetv1.DisaggregatedSet
		featureGates map[featuregate.Feature]bool
		wantErr      error
	}{
		"unmanaged DisaggregatedSet with unsupported features is accepted": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Roles(testingds.MakeRole("prefill").
					ScalingMode(disaggregatedsetv1.RoleScalingExternal).
					SubRoles("a").
					MaxSurge(intstr.FromInt32(1)).
					Label(constants.QueueLabel, "queue")).
				Obj(),
		},
		"managed DisaggregatedSet with supported roles": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(
					testingds.MakeRole("prefill").Replicas(8),
					testingds.MakeRole("decode").WithLeader().Size(4).Replicas(2).MaxSurge(intstr.FromInt32(0)),
				).
				Obj(),
		},
		"invalid queue name": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("Invalid_Queue").
				Roles(testingds.MakeRole("prefill")).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"external role scaling": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").ScalingMode(disaggregatedsetv1.RoleScalingExternal)).
				Obj(),
			wantErr: field.ErrorList{
				field.NotSupported(rolesPath.Index(0).Child("scaling", "mode"), nil, []string{}),
			}.ToAggregate(),
		},
		"sub-roles": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").SubRoles("a", "b")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath.Index(0).Child("subRoles"), ""),
			}.ToAggregate(),
		},
		"LeaderWorkerSet scheduling": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Scheduling(&leaderworkersetv1.LeaderWorkerSetScheduling{})).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath.Index(0).Child("spec", "scheduling"), ""),
			}.ToAggregate(),
		},
		"hash group identity": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").GroupIdentity(leaderworkersetv1.GroupIdentityHash)).
				Obj(),
			wantErr: field.ErrorList{
				field.NotSupported(rolesPath.Index(0).Child("spec", "groupIdentity"), nil, []string{}),
			}.ToAggregate(),
		},
		"max group restarts": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").MaxGroupRestarts(3)).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0LWTPath.Child("maxGroupRestarts"), ""),
			}.ToAggregate(),
		},
		"restart backoff": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").RestartBackoff(10, 300)).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0LWTPath.Child("restartBackoff"), ""),
			}.ToAggregate(),
		},
		"maxSurge greater than zero": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").MaxSurge(intstr.FromInt32(1))).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(maxSurgePath, nil, ""),
			}.ToAggregate(),
		},
		"percentage maxSurge rounds up to one group": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(4).MaxSurge(intstr.FromString("10%"))).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(maxSurgePath, nil, ""),
			}.ToAggregate(),
		},
		"Kueue labels in role metadata and templates": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WithLeader().
					Label(constants.QueueLabel, "queue").
					LeaderTemplateLabel(constants.WorkloadPriorityClassLabel, "high").
					WorkerTemplateLabel(constants.QueueLabel, "queue")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath.Index(0).Child("metadata", "labels").Key(constants.QueueLabel), ""),
				field.Forbidden(role0LeaderMetaPath.Child("labels").Key(constants.WorkloadPriorityClassLabel), ""),
				field.Forbidden(role0WorkerMetaPath.Child("labels").Key(constants.QueueLabel), ""),
			}.ToAggregate(),
		},
		"Kueue annotations in role metadata": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").
					Annotation(kueueconstants.ElasticJobAnnotation, "true").
					Annotation(kueueconstants.ElasticJobScaleUpStrategyAnnotationKey, "atomic").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate").
					Annotation(constants.WaitForPodsReadyAnnotation, `{"timeout":"5m"}`)).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0AnnotationsPath.Key(kueueconstants.ElasticJobAnnotation), ""),
				field.Forbidden(role0AnnotationsPath.Key(kueueconstants.ElasticJobScaleUpStrategyAnnotationKey), ""),
				field.Forbidden(role0AnnotationsPath.Key(kueueconstants.AdmissionGatedByAnnotation), ""),
				field.Forbidden(role0AnnotationsPath.Key(constants.WaitForPodsReadyAnnotation), ""),
			}.ToAggregate(),
		},
		"unmanaged DisaggregatedSet with Kueue annotations in role metadata is accepted": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Roles(testingds.MakeRole("prefill").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate")).
				Obj(),
		},
		"generated PodSet names collide": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("foo").WithLeader(), testingds.MakeRole("foo-leader")).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role1WorkerMetaPath, nil, ""),
			}.ToAggregate(),
		},
		"generated PodSet names are too long": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole(strings.Repeat("a", 60)).WithLeader().Size(2)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0LeaderMetaPath, nil, ""),
				field.Invalid(role0WorkerMetaPath, nil, ""),
			}.ToAggregate(),
		},
		"18 PodSets are accepted": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(
					testingds.MakeRole("role-0").WithLeader().Size(2),
					testingds.MakeRole("role-1").WithLeader().Size(2),
					testingds.MakeRole("role-2").WithLeader().Size(2),
					testingds.MakeRole("role-3").WithLeader().Size(2),
					testingds.MakeRole("role-4").WithLeader().Size(2),
					testingds.MakeRole("role-5").WithLeader().Size(2),
					testingds.MakeRole("role-6").WithLeader().Size(2),
					testingds.MakeRole("role-7").WithLeader().Size(2),
					testingds.MakeRole("role-8"),
					testingds.MakeRole("role-9"),
				).
				Obj(),
		},
		"more than 18 PodSets": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(
					testingds.MakeRole("role-0").WithLeader().Size(2),
					testingds.MakeRole("role-1").WithLeader().Size(2),
					testingds.MakeRole("role-2").WithLeader().Size(2),
					testingds.MakeRole("role-3").WithLeader().Size(2),
					testingds.MakeRole("role-4").WithLeader().Size(2),
					testingds.MakeRole("role-5").WithLeader().Size(2),
					testingds.MakeRole("role-6").WithLeader().Size(2),
					testingds.MakeRole("role-7").WithLeader().Size(2),
					testingds.MakeRole("role-8").WithLeader().Size(2),
					testingds.MakeRole("role-9").WithLeader().Size(2),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.TooMany(rolesPath, 20, jobframework.MaxPodSets),
			}.ToAggregate(),
		},
		"maximum membership at the limit": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(998)).
				Obj(),
		},
		"maximum membership above the limit": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(2).Replicas(499)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"maxUnavailable counts toward the maximum membership": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(990).MaxUnavailable(intstr.FromInt32(10))).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"PodSet count that overflows int32": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(1_000_000).Size(1_000_000)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"role templates above the size limit": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WorkerTemplateAnnotation("example.com/large", strings.Repeat("x", maxTemplateBytes))).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"projected Workload size without TAS annotations within the TAS budget": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(749)).
				Obj(),
		},
		"projected Workload size without TAS annotations above the TAS budget": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(750)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"projected Workload size with TAS above the limit": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(998).
					WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block")).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"projected Workload size with TAS and TAS disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(998).
					WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block")).
				Obj(),
		},
		"elastic job annotation": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Roles(testingds.MakeRole("prefill")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("metadata", "annotations").Key(workloadslicing.EnabledAnnotationKey), ""),
			}.ToAggregate(),
		},
		"placement policy with TAS": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				PlacementPolicy(disaggregatedsetv1.PlacementExclusiveSlice, "cloud.com/block").
				Roles(testingds.MakeRole("prefill").WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(placementPolicyTypePath, ""),
			}.ToAggregate(),
		},
		"placement policy without TAS": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				PlacementPolicy(disaggregatedsetv1.PlacementExclusiveSlice, "cloud.com/block").
				Roles(testingds.MakeRole("prefill")).
				Obj(),
		},
		"placement policy None with TAS": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				PlacementPolicy(disaggregatedsetv1.PlacementNone, "").
				Roles(testingds.MakeRole("prefill").WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block")).
				Obj(),
		},
		"TAS required topology on a role with several groups": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).Replicas(2).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block")).
				Obj(),
		},
		"PodSet group shaped like one LeaderWorkerSet group": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(
					testingds.MakeRole("prefill").WithLeader().Size(3).
						PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
						PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
					testingds.MakeRole("decode").WithLeader().Size(3).
						PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
						PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
				).
				Obj(),
		},
		"PodSet group on a role name that fills the PodSet name": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole(strings.Repeat("a", 56)).WithLeader().Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj(),
		},
		"PodSet group without a leader template": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0WorkerMetaPath.Child("annotations").Key(kueue.PodSetGroupName), ""),
			}.ToAggregate(),
		},
		"PodSet group name only on the leader": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					LeaderTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0LeaderMetaPath.Child("annotations").Key(kueue.PodSetGroupName), nil, ""),
			}.ToAggregate(),
		},
		"PodSet group with size 1": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0LeaderMetaPath.Child("annotations").Key(kueue.PodSetGroupName), ""),
			}.ToAggregate(),
		},
		"PodSet group with several replicas": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).Replicas(2).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0LeaderMetaPath.Child("annotations").Key(kueue.PodSetGroupName), ""),
			}.ToAggregate(),
		},
		"PodSet group with inconsistent topology": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).
					LeaderTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0LeaderMetaPath.Child("annotations"), nil, ""),
				field.Invalid(role0WorkerMetaPath.Child("annotations"), nil, ""),
			}.ToAggregate(),
		},
		"PodSet group and slice size on a DisaggregatedSet scaled to zero": {
			ds: testingds.MakeDisaggregatedSet("ds", "").
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).Replicas(0).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group").
					WorkerTemplateAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/host").
					WorkerTemplateAnnotation(kueue.PodSetSliceSizeAnnotation, "2")).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			w := &Webhook{integrationManager: newTestIntegrationManager(t), client: utiltesting.NewClientBuilder().Build()}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateCreate(ctx, tc.ds)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(admission.Warnings(nil), warns); diff != "" {
				t.Errorf("Unexpected warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	baseDS := testingds.MakeDisaggregatedSet("ds", "ns").
		UID("ds-uid").
		Queue("queue")
	baseRole := testingds.MakeRole("prefill").WithLeader().Size(2).
		Request(corev1.ResourceCPU, "1").
		PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
		PodGroupServingAnnotation()
	unmanagedDS := testingds.MakeDisaggregatedSet("ds", "ns")

	testCases := map[string]struct {
		objs    []runtime.Object
		oldObj  *disaggregatedsetv1.DisaggregatedSet
		newObj  *disaggregatedsetv1.DisaggregatedSet
		wantErr error
	}{
		"no changes": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
		},
		"removing the queue name": {
			oldObj: baseDS.Clone().Roles(baseRole).Obj(),
			newObj: baseDS.Clone().Queue("").Roles(baseRole).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"adding the queue name while a role is Ready": {
			oldObj: unmanagedDS.Clone().Roles(testingds.MakeRole("prefill")).RoleStatus("prefill", 1).Obj(),
			newObj: unmanagedDS.Clone().Queue("queue").Roles(testingds.MakeRole("prefill")).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"adding the queue name while no role is Ready": {
			oldObj: unmanagedDS.Clone().Roles(testingds.MakeRole("prefill")).Obj(),
			newObj: unmanagedDS.Clone().Queue("queue").
				Roles(testingds.MakeRole("prefill").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				Obj(),
		},
		"changing the queue name while a role is Ready": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Queue("other").Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"changing the queue name while no role is Ready": {
			oldObj: baseDS.Clone().Roles(baseRole).Obj(),
			newObj: baseDS.Clone().Queue("other").Roles(baseRole).Obj(),
		},
		"changing the queue name while a slice Workload has reserved quota": {
			objs: []runtime.Object{
				utiltestingapi.MakeWorkload("wl", "ns").
					OwnerReference(gvk, "ds", "ds-uid").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now()).
					Obj(),
			},
			oldObj: baseDS.Clone().Roles(baseRole).Obj(),
			newObj: baseDS.Clone().Queue("other").Roles(baseRole).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"adding a priority class while a role is Ready": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().WorkloadPriorityClass("high").Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(priorityClassLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"removing the priority class": {
			oldObj: baseDS.Clone().WorkloadPriorityClass("high").Roles(baseRole).Obj(),
			newObj: baseDS.Clone().Roles(baseRole).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(priorityClassLabelPath, nil, ""),
			}.ToAggregate(),
		},
		"changing the priority class while a role is Ready": {
			oldObj: baseDS.Clone().WorkloadPriorityClass("high").Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().WorkloadPriorityClass("low").Roles(baseRole).RoleStatus("prefill", 1).Obj(),
		},
		"adding a role": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().
				Roles(baseRole, testingds.MakeRole("decode").
					PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					PodGroupServingAnnotation()).
				RoleStatus("prefill", 1).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"changing the group size": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().Size(3)).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0LWTPath.Child("size"), nil, ""),
			}.ToAggregate(),
		},
		"removing the leader template": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().WithoutLeader()).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0LWTPath.Child("leaderTemplate"), nil, ""),
			}.ToAggregate(),
		},
		"changing the Pod shape": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().Request(corev1.ResourceCPU, "2")).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0LWTPath.Child("leaderTemplate", "spec", "containers").Index(0).Child("resources", "requests"), nil, ""),
				field.Invalid(role0LWTPath.Child("workerTemplate", "spec", "containers").Index(0).Child("resources", "requests"), nil, ""),
			}.ToAggregate(),
		},
		"changing the image": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().Image("new-image")).RoleStatus("prefill", 1).Obj(),
		},
		"changing a TAS annotation": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().
				Roles(baseRole.Clone().WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block")).
				RoleStatus("prefill", 1).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(role0WorkerMetaPath.Child("annotations"), nil, ""),
			}.ToAggregate(),
		},
		"changing replicas while running": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().Replicas(2)).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath, ""),
			}.ToAggregate(),
		},
		"scaling every role to zero": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().Replicas(0)).RoleStatus("prefill", 1).Obj(),
		},
		"scaling up from zero": {
			oldObj: baseDS.Clone().Roles(baseRole.Clone().Replicas(0)).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().Replicas(3)).Obj(),
		},
		"changing slices while running": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Slices(3).Roles(baseRole).RoleStatus("prefill", 1).Obj(),
		},
		"adding an admission gate to role metadata": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().
				Roles(baseRole.Clone().Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate")).
				RoleStatus("prefill", 1).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(role0AnnotationsPath.Key(kueueconstants.AdmissionGatedByAnnotation), ""),
			}.ToAggregate(),
		},
		"raising maxUnavailable above the maximum membership": {
			oldObj: baseDS.Clone().Roles(baseRole).RoleStatus("prefill", 1).Obj(),
			newObj: baseDS.Clone().Roles(baseRole.Clone().MaxUnavailable(intstr.FromInt32(500))).RoleStatus("prefill", 1).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(rolesPath, nil, ""),
			}.ToAggregate(),
		},
		"unmanaged DisaggregatedSet can change anything": {
			oldObj: unmanagedDS.Clone().Roles(testingds.MakeRole("prefill")).RoleStatus("prefill", 1).Obj(),
			newObj: unmanagedDS.Clone().
				Roles(testingds.MakeRole("decode").Size(4).Request(corev1.ResourceCPU, "8")).
				RoleStatus("prefill", 1).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			client := utiltesting.NewClientBuilder().WithRuntimeObjects(tc.objs...).Build()
			w := &Webhook{integrationManager: newTestIntegrationManager(t), client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(admission.Warnings(nil), warns); diff != "" {
				t.Errorf("Unexpected warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func newTestIntegrationManager(t *testing.T) *jobframework.IntegrationManager {
	t.Helper()
	manager := jobframework.NewIntegrationManager()
	for _, registerIntegration := range []func(*jobframework.IntegrationManager) error{RegisterIntegration, pod.RegisterIntegration} {
		if err := registerIntegration(manager); err != nil {
			t.Fatalf("RegisterIntegration() error = %v", err)
		}
	}
	return manager
}
