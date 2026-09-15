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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

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
)

var (
	admissionGatedByAnnotationsPath = field.NewPath("metadata", "annotations").Key(kueueconstants.AdmissionGatedByAnnotation)
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		ds                         *testingds.DisaggregatedSetWrapper
		manageJobsWithoutQueueName bool
		defaultLqExist             bool
		enableIntegrations         []string
		featureGates               map[featuregate.Feature]bool
		wantErr                    error
		wantQueue                  string
		wantLeaderLabels           map[string]string
		wantLeaderAnnotations      map[string]string
		wantWorkerLabels           map[string]string
		wantWorkerAnnotations      map[string]string
	}{
		"queue name copied to leader and worker pod templates": {
			defaultLqExist: true,
			ds: testingds.MakeDisaggregatedSet("test-ds", "default").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue"),
			wantQueue: "test-queue",
			wantLeaderLabels: map[string]string{
				constants.QueueLabel: "test-queue",
			},
			wantWorkerLabels: map[string]string{
				constants.QueueLabel: "test-queue",
			},
			wantLeaderAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			},
			wantWorkerAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
				kueue.PodIndexOffsetAnnotation:           "1",
			},
		},
		"priority class label set on pod templates": {
			defaultLqExist: true,
			ds: testingds.MakeDisaggregatedSet("test-ds", "default").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue").
				WorkloadPriorityClass("high-priority"),
			wantQueue: "test-queue",
			wantLeaderLabels: map[string]string{
				constants.QueueLabel:                 "test-queue",
				constants.WorkloadPriorityClassLabel: "high-priority",
			},
			wantWorkerLabels: map[string]string{
				constants.QueueLabel:                 "test-queue",
				constants.WorkloadPriorityClassLabel: "high-priority",
			},
			wantLeaderAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			},
			wantWorkerAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
				kueue.PodIndexOffsetAnnotation:           "1",
			},
		},
		"SuspendedByParent and GroupServing annotations set": {
			defaultLqExist: true,
			ds: testingds.MakeDisaggregatedSet("test-ds", "default").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			wantQueue: "test-queue",
			wantWorkerLabels: map[string]string{
				constants.QueueLabel: "test-queue",
			},
			wantWorkerAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			},
		},
		"PodIndexOffset set on worker template when TAS enabled and leader template exists": {
			defaultLqExist: true,
			featureGates:   map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			ds: testingds.MakeDisaggregatedSet("test-ds", "default").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue"),
			wantQueue: "test-queue",
			wantLeaderLabels: map[string]string{
				constants.QueueLabel: "test-queue",
			},
			wantLeaderAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			},
			wantWorkerLabels: map[string]string{
				constants.QueueLabel: "test-queue",
			},
			wantWorkerAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
				kueue.PodIndexOffsetAnnotation:           "1",
			},
		},
		"no PodIndexOffset when no leader template": {
			defaultLqExist: true,
			featureGates:   map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			ds: testingds.MakeDisaggregatedSet("test-ds", "default").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			wantQueue: "test-queue",
			wantWorkerLabels: map[string]string{
				constants.QueueLabel: "test-queue",
			},
			wantWorkerAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			},
		},
		"default local queue assigned when no queue label": {
			defaultLqExist: true,
			ds: testingds.MakeDisaggregatedSet("test-ds", "default").
				RoleWithLeader("role-a", 1, 2),
			wantQueue: "default",
			wantLeaderLabels: map[string]string{
				constants.QueueLabel: "default",
			},
			wantLeaderAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			},
			wantWorkerLabels: map[string]string{
				constants.QueueLabel: "default",
			},
			wantWorkerAnnotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: FrameworkName,
				podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
				kueue.PodIndexOffsetAnnotation:           "1",
			},
		},
		"no defaults when no queue and no default lq": {
			defaultLqExist: false,
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			integrationManager := newTestIntegrationManager(t)
			t.Cleanup(integrationManager.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
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

			wh := &Webhook{
				integrationManager:         integrationManager,
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				queues:                     queueManager,
			}

			obj := tc.ds.Obj()
			err := wh.Default(ctx, obj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want, +got):\n%s", diff)
			}
			if err != nil {
				return
			}

			if tc.wantQueue != "" {
				gotQueue := obj.Labels[constants.QueueLabel]
				if gotQueue != tc.wantQueue {
					t.Errorf("Queue label: got %q, want %q", gotQueue, tc.wantQueue)
				}
			}

			for i := range obj.Spec.Roles {
				role := &obj.Spec.Roles[i]
				if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil && tc.wantLeaderLabels != nil {
					for k, v := range tc.wantLeaderLabels {
						if got := role.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels[k]; got != v {
							t.Errorf("Leader label %s: got %q, want %q", k, got, v)
						}
					}
				}
				if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil && tc.wantLeaderAnnotations != nil {
					for k, v := range tc.wantLeaderAnnotations {
						if got := role.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations[k]; got != v {
							t.Errorf("Leader annotation %s: got %q, want %q", k, got, v)
						}
					}
				}
				if tc.wantWorkerLabels != nil {
					for k, v := range tc.wantWorkerLabels {
						if got := role.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels[k]; got != v {
							t.Errorf("Worker label %s: got %q, want %q", k, got, v)
						}
					}
				}
				if tc.wantWorkerAnnotations != nil {
					for k, v := range tc.wantWorkerAnnotations {
						if got := role.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[k]; got != v {
							t.Errorf("Worker annotation %s: got %q, want %q", k, got, v)
						}
					}
					if _, exists := tc.wantWorkerAnnotations[kueue.PodIndexOffsetAnnotation]; !exists {
						if got, ok := role.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[kueue.PodIndexOffsetAnnotation]; ok {
							t.Errorf("Worker annotation %s should not be set, got %q", kueue.PodIndexOffsetAnnotation, got)
						}
					}
				}
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		ds           *testingds.DisaggregatedSetWrapper
		featureGates map[featuregate.Feature]bool
		wantErr      error
		wantWarns    admission.Warnings
	}{
		"valid DS accepted": {
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
		},
		"valid DS without queue accepted": {
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2),
		},
		"invalid queue name rejected": {
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test/queue"),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"AdmissionGatedBy valid single gate": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/my-gate"),
		},
		"AdmissionGatedBy valid multiple gates": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/my-gate,example.com/other-gate"),
		},
		"AdmissionGatedBy invalid format": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "invalid_gate_name"),
			wantErr: field.ErrorList{
				field.Invalid(admissionGatedByAnnotationsPath, "invalid_gate_name", ""),
			}.ToAggregate(),
		},
		"AdmissionGatedBy disabled - invalid value passes": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: false},
			ds: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "this is invalid"),
			wantErr: nil,
		},
		"too many PodSets rejected": {
			ds: func() *testingds.DisaggregatedSetWrapper {
				w := testingds.MakeDisaggregatedSet("test-ds", "").Queue("test-queue")
				for i := range jobframework.MaxPodSets + 1 {
					w.Role(fmt.Sprintf("role-%d", i), 1, 1)
				}
				return w
			}(),
			wantErr: field.ErrorList{
				field.TooMany(rolesPath, jobframework.MaxPodSets+1, jobframework.MaxPodSets),
			}.ToAggregate(),
		},
		"max PodSets accepted": {
			ds: func() *testingds.DisaggregatedSetWrapper {
				w := testingds.MakeDisaggregatedSet("test-ds", "").Queue("test-queue")
				for i := range jobframework.MaxPodSets {
					w.Role(fmt.Sprintf("role-%d", i), 1, 1)
				}
				return w
			}(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			integrationManager := newTestIntegrationManager(t)
			t.Cleanup(integrationManager.EnableIntegrationsForTest(t, "pod"))
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			wh := &Webhook{integrationManager: integrationManager, client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := wh.ValidateCreate(ctx, tc.ds.Obj())
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
		oldObj       *testingds.DisaggregatedSetWrapper
		newObj       *testingds.DisaggregatedSetWrapper
		featureGates map[featuregate.Feature]bool
		wantErr      error
	}{
		"no changes": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
		},
		"queue name immutable when not suspended (has ready replicas)": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				RoleStatus("role-a", 1, 1),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("new-queue").
				RoleStatus("role-a", 1, 1),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"queue name change allowed when suspended (all ReadyReplicas == 0)": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				RoleStatus("role-a", 1, 0),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("new-queue").
				RoleStatus("role-a", 1, 0),
		},
		"queue name delete rejected": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"pod template immutable while managed - change resources": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				RoleRequest(corev1.ResourceCPU, "1").
				Queue("test-queue"),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: rolesPath.Index(0).Child("spec", "leaderWorkerTemplate", "workerTemplate", "spec", "containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"pod template immutable while managed - change leader resources": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue"),
		},
		"priority class change allowed when suspended": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test"),
		},
		"priority class set rejected when not suspended": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				RoleStatus("role-a", 1, 1),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				RoleStatus("role-a", 1, 1),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/priority-class]",
				},
			}.ToAggregate(),
		},
		"priority class delete rejected when not suspended": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				RoleStatus("role-a", 1, 1),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				RoleStatus("role-a", 1, 1),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/priority-class]",
				},
			}.ToAggregate(),
		},
		"AdmissionGatedBy - reject adding gates after creation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/my-gate"),
			wantErr: field.ErrorList{
				field.Forbidden(admissionGatedByAnnotationsPath, "can only remove gates, not add new ones"),
			}.ToAggregate(),
		},
		"AdmissionGatedBy - allow removing gates": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/my-gate"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
		},
		"reject adding a role while managed": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Role("role-b", 1, 2).
				Queue("test-queue"),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath, ""),
			}.ToAggregate(),
		},
		"reject removing a role while managed": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Role("role-b", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath, ""),
			}.ToAggregate(),
		},
		"reject renaming a role while managed": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-b", 1, 2).
				Queue("test-queue"),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath.Index(0).Child("name"), ""),
			}.ToAggregate(),
		},
		"reject adding leader template while managed": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue"),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath.Index(0).Child("spec", "leaderWorkerTemplate", "leaderTemplate"), ""),
			}.ToAggregate(),
		},
		"reject removing leader template while managed": {
			oldObj: testingds.MakeDisaggregatedSet("test-ds", "").
				RoleWithLeader("role-a", 1, 2).
				Queue("test-queue"),
			newObj: testingds.MakeDisaggregatedSet("test-ds", "").
				Role("role-a", 1, 2).
				Queue("test-queue"),
			wantErr: field.ErrorList{
				field.Forbidden(rolesPath.Index(0).Child("spec", "leaderWorkerTemplate", "leaderTemplate"), ""),
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			integrationManager := newTestIntegrationManager(t)
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			wh := &Webhook{integrationManager: integrationManager}

			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := wh.ValidateUpdate(ctx, tc.oldObj.Obj(), tc.newObj.Obj())
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
