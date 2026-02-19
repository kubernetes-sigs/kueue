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

package mpijob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

const (
	invalidRFC1123Message = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
)

var (
	labelsPath         = field.NewPath("metadata", "labels")
	queueNameLabelPath = labelsPath.Key(constants.QueueLabel)
)

func TestValidateCreate(t *testing.T) {
	testcases := []struct {
		name                    string
		job                     *v2beta1.MPIJob
		wantErr                 error
		topologyAwareScheduling bool
	}{
		{
			name:    "simple",
			job:     testingutil.MakeMPIJob("job", "default").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name:    "invalid queue-name label",
			job:     testingutil.MakeMPIJob("job", "default").Queue("queue_name").Obj(),
			wantErr: field.ErrorList{field.Invalid(queueNameLabelPath, "queue_name", invalidRFC1123Message)}.ToAggregate(),
		},
		{
			name:    "with prebuilt workload",
			job:     testingutil.MakeMPIJob("job", "default").Queue("queue").Label(constants.PrebuiltWorkloadLabel, "prebuilt-workload").Obj(),
			wantErr: nil,
		},
		{
			name: "valid topology request",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
				field.Invalid(
					field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid slice topology request - slice size larger than number of podsets",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetSliceSizeAnnotation, "20").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetSliceSizeAnnotation, "20").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "20", "must not be greater than pod set count 1"),
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "20", "must not be greater than pod set count 3"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		{
			name: "valid PodSet grouping request",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid PodSet grouping request - groups of size other than 2",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "groupname1").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetGroupName, "groupname2").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "groupname1", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "groupname2", "can only define groups of exactly 2 pod sets, got: 1 pod set(s)"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid PodSet grouping request - no leader in group",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 2,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "groupname", "can only define groups where at least one pod set has only 1 replica, got: 2 replica(s) and 3 replica(s) in the group"),
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-group-name"), "groupname", "can only define groups where at least one pod set has only 1 replica, got: 2 replica(s) and 3 replica(s) in the group"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid PodSet grouping request - required topology does not match",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/rack").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.mpiReplicaSpecs[Worker].template.metadata.annotations' in group 'groupname'"),
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.mpiReplicaSpecs[Launcher].template.metadata.annotations' in group 'groupname'"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid PodSet grouping request - preferred topology does not match",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetPreferredTopologyAnnotation, "cloud.com/rack").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.mpiReplicaSpecs[Worker].template.metadata.annotations' in group 'groupname'"),
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.mpiReplicaSpecs[Launcher].template.metadata.annotations' in group 'groupname'"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		{
			name: "invalid PodSet grouping request - different topology annotations within group",
			job: testingutil.MakeMPIJob("job", "default").
				Queue("queue-name").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetGroupName, "groupname").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodSetPreferredTopologyAnnotation, "cloud.com/rack").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Launcher].template.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.mpiReplicaSpecs[Worker].template.metadata.annotations' in group 'groupname'"),
				field.Invalid(field.NewPath("spec.mpiReplicaSpecs[Worker].template.metadata.annotations"), field.OmitValueType{}, "must specify 'kueue.x-k8s.io/podset-required-topology' or 'kueue.x-k8s.io/podset-preferred-topology' topology consistent with 'spec.mpiReplicaSpecs[Launcher].template.metadata.annotations' in group 'groupname'"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.topologyAwareScheduling)

			jsw := &MpiJobWebhook{}
			ctx, _ := utiltesting.ContextWithLog(t)
			_, gotErr := jsw.ValidateCreate(ctx, tc.job)

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	testCases := []struct {
		name                    string
		mpiJob                  *v2beta1.MPIJob
		queues                  []kueue.LocalQueue
		clusterQueues           []kueue.ClusterQueue
		admissionCheck          *kueue.AdmissionCheck
		multiKueueEnabled       bool
		topologyAwareScheduling bool
		defaultLqExist          bool
		want                    *v2beta1.MPIJob
		wantManagedBy           *string
		wantErr                 error
	}{
		{
			name: "TestDefault_MPIJobManagedBy_mpijobapi.MPIJobControllerName",
			mpiJob: &v2beta1.MPIJob{
				Spec: v2beta1.MPIJobSpec{
					RunPolicy: v2beta1.RunPolicy{
						ManagedBy: ptr.To(v2beta1.KubeflowJobController),
					},
				},
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltestingapi.MakeAdmissionCheck("admission-check").
				ControllerName(kueue.MultiKueueControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: true,
			wantManagedBy:     ptr.To(kueue.MultiKueueControllerName),
		},
		{
			name: "TestDefault_WithQueueLabel",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltestingapi.MakeAdmissionCheck("admission-check").
				ControllerName(kueue.MultiKueueControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: true,
			wantManagedBy:     ptr.To(kueue.MultiKueueControllerName),
		},
		{
			name: "TestDefault_WithoutQueueLabel",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{Namespace: "default"},
			},
			multiKueueEnabled: true,
			wantManagedBy:     nil,
		},
		{
			name: "TestDefault_InvalidQueueName",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{
					Labels:    map[string]string{constants.QueueLabel: "invalid-queue"},
					Namespace: "default",
				},
			},
			multiKueueEnabled: true,
		},
		{
			name: "TestDefault_QueueNotFound",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "non-existent-queue",
					},
					Namespace: "default",
				},
			},
			multiKueueEnabled: true,
		},
		{
			name: "TestDefault_AdmissionCheckNotFound",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cluster-queue").
					AdmissionChecks("non-existent-admission-check").
					Obj(),
			},
			multiKueueEnabled: true,
			wantManagedBy:     nil,
		},
		{
			name: "TestDefault_MultiKueueFeatureDisabled",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltestingapi.MakeAdmissionCheck("admission-check").
				ControllerName(kueue.MultiKueueControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: false,
			wantManagedBy:     nil,
		},
		{
			name: "TestDefault_UserSpecifiedManagedBy",
			mpiJob: &v2beta1.MPIJob{
				Spec: v2beta1.MPIJobSpec{
					RunPolicy: v2beta1.RunPolicy{
						ManagedBy: ptr.To("example.com/foo"),
					},
				},
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltestingapi.MakeAdmissionCheck("admission-check").
				ControllerName(kueue.MultiKueueControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: true,
			wantManagedBy:     ptr.To("example.com/foo"),
		},
		{
			name: "TestDefault_ClusterQueueWithoutAdmissionCheck",
			mpiJob: &v2beta1.MPIJob{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cluster-queue").
					Obj(),
			},
			multiKueueEnabled: true,
			wantManagedBy:     nil,
		},
		{
			name:           "default lq is created, job doesn't have queue label",
			defaultLqExist: true,
			mpiJob:         testingutil.MakeMPIJob("job", "default").Obj(),
			want:           testingutil.MakeMPIJob("job", "default").Queue("default").Obj(),
		},
		{
			name:           "default lq is created, job has queue label",
			defaultLqExist: true,
			mpiJob:         testingutil.MakeMPIJob("job", "default").Queue("queue").Obj(),
			want:           testingutil.MakeMPIJob("job", "default").Queue("queue").Obj(),
		},
		{
			name:           "default lq isn't created, job doesn't have queue label",
			defaultLqExist: false,
			mpiJob:         testingutil.MakeMPIJob("job", "default").Obj(),
			want:           testingutil.MakeMPIJob("job", "default").Obj(),
		},
		{
			name:                    "TAS enabled, RunLauncherAsWorker true with 2 replica specs",
			topologyAwareScheduling: true,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodIndexOffsetAnnotation, "1").
				Obj(),
		},
		{
			name:                    "TAS enabled, RunLauncherAsWorker false",
			topologyAwareScheduling: true,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(false).
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(false).
				Obj(),
		},
		{
			name:                    "TAS enabled, RunLauncherAsWorker nil",
			topologyAwareScheduling: true,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				Obj(),
		},
		{
			name:                    "TAS disabled, RunLauncherAsWorker true",
			topologyAwareScheduling: false,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				Obj(),
		},
		{
			name:                    "TAS enabled, RunLauncherAsWorker true, only Launcher replica",
			topologyAwareScheduling: true,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				GenericLauncher().
				RunLauncherAsWorker(true).
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				GenericLauncher().
				RunLauncherAsWorker(true).
				Obj(),
		},
		{
			name:                    "TAS enabled, RunLauncherAsWorker true, Worker has existing annotations",
			topologyAwareScheduling: true,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, "existing-annotation", "value").
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, "existing-annotation", "value").
				PodAnnotation(v2beta1.MPIReplicaTypeWorker, kueue.PodIndexOffsetAnnotation, "1").
				Obj(),
		},
		{
			name:                    "TAS enabled, RunLauncherAsWorker true, Launcher has PodSetGroupName annotation",
			topologyAwareScheduling: true,
			mpiJob: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "group1").
				Obj(),
			want: testingutil.MakeMPIJob("job", "default").
				Queue("queue").
				MPIJobReplicaSpecs(
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
					},
					testingutil.MPIJobReplicaSpecRequirement{
						ReplicaType:  v2beta1.MPIReplicaTypeWorker,
						ReplicaCount: 3,
					},
				).
				RunLauncherAsWorker(true).
				PodAnnotation(v2beta1.MPIReplicaTypeLauncher, kueue.PodSetGroupName, "group1").
				Obj(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.multiKueueEnabled)
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.topologyAwareScheduling)

			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("default"))
			cl := clientBuilder.Build()
			cqCache := schdcache.New(cl)
			queueManager := qcache.NewManagerForUnitTests(cl, cqCache)

			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

			for _, q := range tc.queues {
				if err := queueManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if tc.admissionCheck != nil {
					cqCache.AddOrUpdateAdmissionCheck(log, tc.admissionCheck)
					if err := queueManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
				}
			}
			webhook := &MpiJobWebhook{
				manageJobsWithoutQueueName: false,
				queues:                     queueManager,
				cache:                      cqCache,
			}

			gotErr := webhook.Default(ctx, tc.mpiJob)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Default() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantManagedBy, tc.mpiJob.Spec.RunPolicy.ManagedBy); diff != "" {
				t.Errorf("Default() mpijob.Spec.RunPolicy.ManagedBy mismatch (-want +got):\n%s", diff)
			}
			if tc.want != nil {
				if diff := cmp.Diff(tc.want, tc.mpiJob); diff != "" {
					t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
