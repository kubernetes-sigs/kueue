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

package pod

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	basePodName := "wl1"
	basePodBuilder := utiltestingpod.MakePod(basePodName, TestNamespace)

	groupSize := 3
	podGroup := basePodBuilder.
		Clone().
		MakePodGroupWrappers(groupSize)

	podGroupWithWl := basePodBuilder.
		Clone().
		PrebuiltWorkloadLabel("wl1").
		Label(kueue.MultiKueueOriginLabel, "origin1").
		MakePodGroupWrappers(groupSize)
	for i := range podGroupWithWl {
		podGroupWithWl[i].UID(fmt.Sprintf("worker-pod-%d", i))
	}

	podGroupWithWlAnnotations := basePodBuilder.
		Clone().
		MakePodGroupWrappersWithWorkloadAnnotations(groupSize)

	workerPodGroupWithAnnotations := basePodBuilder.
		Clone().
		PrebuiltWorkloadAnnotation("wl1").
		Label(kueue.MultiKueueOriginLabel, "origin1").
		MakePodGroupWrappersWithWorkloadAnnotations(groupSize)
	for i := range workerPodGroupWithAnnotations {
		workerPodGroupWithAnnotations[i].UID(fmt.Sprintf("worker-pod-%d", i))
	}
	groupWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
		Name:      "wl1",
		Namespace: TestNamespace,
		Annotations: map[string]string{
			podconstants.IsGroupWorkloadAnnotationKey: podconstants.IsGroupWorkloadAnnotationValue,
		},
		OwnerReferences: []metav1.OwnerReference{
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       podGroup[0].Obj().Name,
				UID:        "manager-anchor-uid",
				Controller: new(true),
			},
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       podGroup[1].Obj().Name,
				UID:        types.UID("manager-" + podGroup[1].Obj().Name + "-uid"),
			},
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       podGroup[2].Obj().Name,
				UID:        types.UID("manager-" + podGroup[2].Obj().Name + "-uid"),
			},
		},
	}}

	cases := map[string]struct {
		managersPods []corev1.Pod
		workerPods   []corev1.Pod

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError        error
		wantManagersPods []corev1.Pod
		wantWorkerPods   []corev1.Pod
		featureGates     map[featuregate.Feature]bool
		ignoreWorkerUIDs bool
	}{
		"sync creates missing remote pod": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote pod": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"keeps SchedulingGated condition on gated manager pod (avoids spurious autoscaler scale-up)": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodPending).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonUnschedulable,
					}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			// The remote PodScheduled=False/Unschedulable condition must not overwrite
			// the local SchedulingGated one, otherwise the management cluster's
			// cluster-autoscaler treats the gated Pod as unschedulable and scales up.
			// Other status (the phase here) is still synced from the worker.
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusPhase(corev1.PodPending).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodPending).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonUnschedulable,
					}).
					Obj(),
			},
		},
		"overwrites SchedulingGated once the remote pod is scheduled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			// While the worker Pod is unscheduled the manager Pod keeps its local
			// SchedulingGated condition (see the case above, which avoids a spurious
			// autoscaler scale-up). Once the worker Pod reports PodScheduled=True, that
			// condition is synced through to the manager Pod so its status reflects
			// reality instead of showing SchedulingGated while the phase is Running.
			// The manager Pod stays gated in its spec.
			managersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(
						corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusPhase(corev1.PodRunning).
					StatusConditions(
						corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(
						corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					).
					Obj(),
			},
		},
		"remote pod is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{*basePodBuilder.DeepCopy()},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace})
			},
			wantManagersPods: []corev1.Pod{*basePodBuilder.DeepCopy()},
		},
		"pod managedBy multikueue": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
		},
		"sync creates missing remote pods of the group": {
			featureGates:     map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			ignoreWorkerUIDs: true,
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].DeepCopy(),
			},
		},
		"sync status from remote pod group": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"sync rejects remote pod group member from another workload": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					PrebuiltWorkloadLabel("another-workload").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					PrebuiltWorkloadLabel("another-workload").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"sync rejects remote pod with another group identity": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					GroupNameLabel("another-group").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					GroupNameLabel("another-group").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"sync status from annotation pod group after gate disable": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*workerPodGroupWithAnnotations[0].DeepCopy(),
				*workerPodGroupWithAnnotations[1].DeepCopy(),
				*workerPodGroupWithAnnotations[2].Clone().
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].Clone().
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*workerPodGroupWithAnnotations[0].DeepCopy(),
				*workerPodGroupWithAnnotations[1].DeepCopy(),
				*workerPodGroupWithAnnotations[2].Clone().
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"sync status from label pod group after gate enable": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].Clone().
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"sync rejects conflicting remote anchor group identities": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().GroupNameAnnotation("another-group").Obj(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().GroupNameAnnotation("another-group").Obj(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].DeepCopy(),
			},
		},
		"sync rejects conflicting remote member group identities": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().GroupNameAnnotation("another-group").Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().GroupNameAnnotation("another-group").Obj(),
			},
		},
		"sync rejects unlabelled remote pod group member": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*utiltestingpod.MakePod(podGroup[2].Obj().Name, TestNamespace).
					UID("victim-uid").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*utiltestingpod.MakePod(podGroup[2].Obj().Name, TestNamespace).
					UID("victim-uid").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"remote pod group is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*workerPodGroupWithAnnotations[0].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*workerPodGroupWithAnnotations[1].DeepCopy(),
				*workerPodGroupWithAnnotations[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					groupWorkload,
					"origin1",
				)
			},
			wantManagersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
		},
		"remote pod group cleanup preserves unlabelled member": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].DeepCopy(),
				*utiltestingpod.MakePod(podGroup[2].Obj().Name, TestNamespace).
					UID("victim-uid").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					groupWorkload,
					"origin1",
				)
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*utiltestingpod.MakePod(podGroup[2].Obj().Name, TestNamespace).
					UID("victim-uid").
					Obj(),
			},
		},
		"remote pod group cleanup uses Workload context after manager Pods are finalized": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-"+podGroupWithWl[1].Obj().Name+"-uid").Obj(),
				*utiltestingpod.MakePod(podGroup[2].Obj().Name, TestNamespace).
					UID("victim-uid").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					groupWorkload,
					"origin1",
				)
			},
			wantWorkerPods: []corev1.Pod{
				*utiltestingpod.MakePod(podGroup[2].Obj().Name, TestNamespace).
					UID("victim-uid").
					Obj(),
			},
		},
		"remote pod group cleanup does not trust a live manager member absent from the Workload": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				workloadWithoutMember := groupWorkload.DeepCopy()
				workloadWithoutMember.OwnerReferences = workloadWithoutMember.OwnerReferences[:1]
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					workloadWithoutMember,
					"origin1",
				)
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
			},
		},
		"remote pod group cleanup preserves member from another workload": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					PrebuiltWorkloadLabel("another-workload").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					groupWorkload,
					"origin1",
				)
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[2].Clone().
					PrebuiltWorkloadLabel("another-workload").
					Obj(),
			},
		},
		"remote pod group cleanup rejects a member with another manager UID": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "foreign-manager-pod-uid").Obj(),
				*podGroupWithWl[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					groupWorkload,
					"origin1",
				)
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].Clone().Annotation(kueue.MultiKueueOriginUIDAnnotation, "foreign-manager-pod-uid").Obj(),
				*podGroupWithWl[2].DeepCopy(),
			},
		},
		"remote pod group cleanup preserves foreign anchor": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().PrebuiltWorkloadLabel("another-workload").Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].Clone().PrebuiltWorkloadLabel("another-workload").Obj(),
				*podGroupWithWl[2].Clone().PrebuiltWorkloadLabel("another-workload").Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return jobframework.DeleteRemoteObjectForWorkloadIfOwned(
					ctx,
					managerClient,
					workerClient,
					adapter,
					types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace},
					groupWorkload,
					"origin1",
				)
			},
			wantError: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().PrebuiltWorkloadLabel("another-workload").Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").Obj(),
				*podGroupWithWl[1].Clone().PrebuiltWorkloadLabel("another-workload").Obj(),
				*podGroupWithWl[2].Clone().PrebuiltWorkloadLabel("another-workload").Obj(),
			},
		},
		"sync creates missing remote pod, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync creates missing remote pods of the group, WorkloadIdentifierAnnotations enabled": {
			featureGates:     map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			ignoreWorkerUIDs: true,
			managersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*workerPodGroupWithAnnotations[0].DeepCopy(),
				*workerPodGroupWithAnnotations[1].DeepCopy(),
				*workerPodGroupWithAnnotations[2].DeepCopy(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerPodUIDs := make(map[string]types.UID, len(tc.managersPods))
			setManagerPodUIDs := func(pods []corev1.Pod) {
				for i := range pods {
					if pods[i].UID == "" {
						if pods[i].Name == podGroup[0].Obj().Name {
							pods[i].UID = "manager-anchor-uid"
						} else {
							pods[i].UID = types.UID("manager-" + pods[i].Name + "-uid")
						}
					}
					managerPodUIDs[pods[i].Name] = pods[i].UID
				}
			}
			setManagerPodUIDs(tc.managersPods)
			setManagerPodUIDs(tc.wantManagersPods)
			initialWorkerNames := make(map[string]struct{}, len(tc.workerPods))
			setRemotePodUIDs := func(pods []corev1.Pod, recordInitial bool) {
				for i := range pods {
					if recordInitial {
						initialWorkerNames[pods[i].Name] = struct{}{}
					} else if _, existed := initialWorkerNames[pods[i].Name]; !existed {
						continue
					}
					managerUID := managerPodUIDs[pods[i].Name]
					if managerUID == "" || pods[i].Labels[kueue.MultiKueueOriginLabel] != "origin1" {
						continue
					}
					if pods[i].Annotations == nil {
						pods[i].Annotations = make(map[string]string, 1)
					}
					if pods[i].Annotations[kueue.MultiKueueOriginUIDAnnotation] == "" {
						pods[i].Annotations[kueue.MultiKueueOriginUIDAnnotation] = string(managerUID)
					}
				}
			}
			setRemotePodUIDs(tc.workerPods, true)
			setRemotePodUIDs(tc.wantWorkerPods, false)

			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			managerBuilder := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithIndex(&corev1.Pod{}, multiKueuePodGroupNameCacheKey, indexMultiKueuePodGroupName)
			managerBuilder = managerBuilder.WithLists(&corev1.PodList{Items: tc.managersPods})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersPods, func(w *corev1.Pod) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithIndex(&corev1.Pod{}, multiKueuePodGroupNameCacheKey, indexMultiKueuePodGroupName)
			workerBuilder = workerBuilder.WithLists(&corev1.PodList{Items: tc.workerPods})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotmanagersPods := &corev1.PodList{}
			if err := managerClient.List(ctx, gotmanagersPods); err != nil {
				t.Errorf("unexpected list manager's pods error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersPods, gotmanagersPods.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's pods (-want/+got):\n%s", diff)
				}
			}

			gotWorkerPods := &corev1.PodList{}
			if err := workerClient.List(ctx, gotWorkerPods); err != nil {
				t.Errorf("unexpected list worker's pod error %s", err)
			} else {
				workerCheckOpts := append(cmp.Options{}, objCheckOpts...)
				if tc.ignoreWorkerUIDs {
					workerCheckOpts = append(workerCheckOpts, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID"))
				}
				if diff := cmp.Diff(tc.wantWorkerPods, gotWorkerPods.Items, workerCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's pod (-want/+got):\n%s", diff)
				}
			}
		})
	}
}

func TestSyncPodGroupRejectsSameNameManagerReplacement(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false})
	const (
		origin       = "origin"
		workloadName = "workload"
	)
	pods := utiltestingpod.MakePod(workloadName, TestNamespace).MakePodGroupWrappers(2)
	anchor := pods[0].UID("original-anchor-uid").Obj()
	replacementMember := pods[1].UID("replacement-member-uid").Obj()
	originalMemberUID := types.UID("original-member-uid")
	key := client.ObjectKeyFromObject(anchor)

	managerWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
		Name:      workloadName,
		Namespace: TestNamespace,
		UID:       "manager-workload-uid",
		Annotations: map[string]string{
			podconstants.IsGroupWorkloadAnnotationKey: podconstants.IsGroupWorkloadAnnotationValue,
		},
		OwnerReferences: []metav1.OwnerReference{
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       anchor.Name,
				UID:        anchor.UID,
				Controller: new(true),
			},
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       replacementMember.Name,
				UID:        originalMemberUID,
			},
		},
	}}
	remoteAnchor := anchor.DeepCopy()
	remoteAnchor.UID = "worker-anchor-uid"
	jobframework.SetMultiKueueMeta(remoteAnchor, workloadName, origin)
	remoteAnchor.Annotations[kueue.MultiKueueOriginUIDAnnotation] = string(anchor.UID)

	managerClient := utiltesting.NewClientBuilder().
		WithObjects(anchor, replacementMember).
		WithStatusSubresource(anchor, replacementMember).
		WithIndex(&corev1.Pod{}, multiKueuePodGroupNameCacheKey, indexMultiKueuePodGroupName).
		Build()
	workerClient := utiltesting.NewClientBuilder().WithObjects(remoteAnchor).Build()
	ctx, _ := utiltesting.ContextWithLog(t)

	_, err := jobframework.SyncJobWithRemoteObjectOwnership(
		ctx,
		managerClient,
		managerClient,
		workerClient,
		&multiKueueAdapter{},
		key,
		managerWorkload,
		origin,
	)
	if !errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
		t.Fatalf("SyncJobWithRemoteObjectOwnership() error = %v, want %v", err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue)
	}
	remoteMember := &corev1.Pod{}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(replacementMember), remoteMember); !apierrors.IsNotFound(err) {
		t.Fatalf("worker replacement-member Pod Get() error = %v, want NotFound", err)
	}
}

func TestMultiKueueWorkloadKeysForSurvivesFeatureGateChanges(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false})
	pod := utiltestingpod.MakePod("pod", TestNamespace).
		PrebuiltWorkloadAnnotation("workload").
		Obj()

	got, err := (&multiKueueAdapter{}).WorkloadKeysFor(pod)
	if err != nil {
		t.Fatalf("WorkloadKeysFor() error = %v", err)
	}
	want := []types.NamespacedName{{Name: "workload", Namespace: TestNamespace}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("WorkloadKeysFor() (-want,+got):\n%s", diff)
	}
}

func TestDeleteRemotePodGroupPaginationKeepsAnchorForRetry(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	const (
		groupName = "workload"
		origin    = "origin"
	)
	anchor := utiltestingpod.MakePod("anchor", TestNamespace).
		UID("anchor-uid").
		GroupNameLabel(groupName).
		PrebuiltWorkloadLabel(groupName).
		Label(kueue.MultiKueueOriginLabel, origin).
		Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-anchor-uid").
		Obj()
	member1 := utiltestingpod.MakePod("member-1", TestNamespace).
		UID("member-1-uid").
		GroupNameLabel(groupName).
		PrebuiltWorkloadLabel(groupName).
		Label(kueue.MultiKueueOriginLabel, origin).
		Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-member-1-uid").
		Obj()
	member2 := utiltestingpod.MakePod("member-2", TestNamespace).
		UID("member-2-uid").
		GroupNameLabel(groupName).
		PrebuiltWorkloadLabel(groupName).
		Label(kueue.MultiKueueOriginLabel, origin).
		Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-member-2-uid").
		Obj()
	collision := utiltestingpod.MakePod("collision", TestNamespace).
		UID("collision-uid").
		GroupNameLabel(groupName).
		PrebuiltWorkloadLabel("another-workload").
		Label(kueue.MultiKueueOriginLabel, origin).
		Obj()
	foreignOrigin := utiltestingpod.MakePod("foreign-origin", TestNamespace).
		UID("foreign-origin-uid").
		GroupNameLabel(groupName).
		PrebuiltWorkloadLabel(groupName).
		Label(kueue.MultiKueueOriginLabel, "another-origin").
		Obj()

	baseClient := utiltesting.NewClientBuilder().WithObjects(anchor, member1, member2, collision, foreignOrigin).Build()
	listCall := 0
	pageErr := errors.New("second page unavailable")
	workerClient := interceptor.NewClient(baseClient, interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			listCall++
			listOptions := &client.ListOptions{}
			for _, opt := range opts {
				opt.ApplyToList(listOptions)
			}
			if listOptions.Namespace != TestNamespace {
				t.Errorf("List() namespace = %q, want %q", listOptions.Namespace, TestNamespace)
			}
			if listOptions.Limit != remotePodCleanupPageSize {
				t.Errorf("List() limit = %d, want %d", listOptions.Limit, remotePodCleanupPageSize)
			}
			wantSelector := kueue.MultiKueueOriginLabel + "=" + origin
			if listOptions.LabelSelector == nil || listOptions.LabelSelector.String() != wantSelector {
				t.Errorf("List() selector = %v, want %q", listOptions.LabelSelector, wantSelector)
			}
			podList, ok := list.(*corev1.PodList)
			if !ok {
				t.Fatalf("List() object = %T, want *corev1.PodList", list)
			}
			switch listCall {
			case 1:
				*podList = corev1.PodList{
					ListMeta: metav1.ListMeta{Continue: "page-2"},
					Items:    []corev1.Pod{*anchor.DeepCopy(), *member1.DeepCopy()},
				}
			case 2:
				if listOptions.Continue != "page-2" {
					t.Errorf("second List() continue = %q, want page-2", listOptions.Continue)
				}
				return pageErr
			case 3:
				*podList = corev1.PodList{
					ListMeta: metav1.ListMeta{Continue: "page-2"},
					Items:    []corev1.Pod{*anchor.DeepCopy(), *member1.DeepCopy()},
				}
			case 4:
				if listOptions.Continue != "page-2" {
					t.Errorf("retry second List() continue = %q, want page-2", listOptions.Continue)
				}
				*podList = corev1.PodList{Items: []corev1.Pod{*member2.DeepCopy(), *collision.DeepCopy()}}
			default:
				t.Fatalf("unexpected List() call %d", listCall)
			}
			return nil
		},
	})

	managerAnchor := anchor.DeepCopy()
	managerAnchor.UID = "manager-anchor-uid"
	managerMember1 := member1.DeepCopy()
	managerMember1.UID = "manager-member-1-uid"
	managerMember2 := member2.DeepCopy()
	managerMember2.UID = "manager-member-2-uid"
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerAnchor, managerMember1, managerMember2).Build()
	localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
		Name:      groupName,
		Namespace: TestNamespace,
		Annotations: map[string]string{
			podconstants.IsGroupWorkloadAnnotationKey: podconstants.IsGroupWorkloadAnnotationValue,
		},
		OwnerReferences: []metav1.OwnerReference{
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       anchor.Name,
				UID:        "manager-anchor-uid",
				Controller: new(true),
			},
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       member1.Name,
				UID:        "manager-member-1-uid",
			},
			{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Name:       member2.Name,
				UID:        "manager-member-2-uid",
			},
		},
	}}
	adapter := &multiKueueAdapter{}
	key := client.ObjectKeyFromObject(anchor)

	err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, managerClient, workerClient, adapter, key, localWorkload, origin)
	if !errors.Is(err, pageErr) {
		t.Fatalf("first cleanup error = %v, want %v", err, pageErr)
	}
	if err := workerClient.Get(ctx, key, &corev1.Pod{}); err != nil {
		t.Fatalf("anchor was deleted before all pages succeeded: %v", err)
	}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(member1), &corev1.Pod{}); err != nil {
		t.Fatalf("first-page member was deleted before all pages were authenticated: %v", err)
	}

	if err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, managerClient, workerClient, adapter, key, localWorkload, origin); err != nil {
		t.Fatalf("retry cleanup error = %v", err)
	}
	for _, deletedPod := range []*corev1.Pod{anchor, member1, member2} {
		if err := workerClient.Get(ctx, client.ObjectKeyFromObject(deletedPod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
			t.Errorf("deleted Pod %q Get() error = %v, want NotFound", deletedPod.Name, err)
		}
	}
	for _, preservedPod := range []*corev1.Pod{collision, foreignOrigin} {
		got := &corev1.Pod{}
		if err := workerClient.Get(ctx, client.ObjectKeyFromObject(preservedPod), got); err != nil {
			t.Errorf("preserved Pod %q Get() error = %v", preservedPod.Name, err)
		} else if got.UID != preservedPod.UID {
			t.Errorf("preserved Pod %q UID = %q, want %q", preservedPod.Name, got.UID, preservedPod.UID)
		}
	}
}

func TestDeleteRemotePodGroupRejectsReplacementBeforePageDeletion(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	const (
		groupName = "workload"
		origin    = "origin"
	)
	makePod := func(name string, uid types.UID) *corev1.Pod {
		return utiltestingpod.MakePod(name, TestNamespace).
			UID(string(uid)).
			GroupNameLabel(groupName).
			PrebuiltWorkloadLabel(groupName).
			Label(kueue.MultiKueueOriginLabel, origin).
			Obj()
	}
	anchor := makePod("anchor", "anchor-uid")
	anchor.Annotations = map[string]string{kueue.MultiKueueOriginUIDAnnotation: "manager-anchor-uid"}
	member := makePod("member", "member-uid")
	replacementAnchor := makePod(anchor.Name, "replacement-anchor-uid")
	replacementMember := makePod(member.Name, "replacement-member-uid")

	baseClient := utiltesting.NewClientBuilder().WithObjects(anchor, member).Build()
	listCalled := false
	workerClient := interceptor.NewClient(baseClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
			if listCalled {
				t.Fatal("unexpected second List() call")
			}
			listCalled = true
			if err := c.Delete(ctx, anchor); err != nil {
				return err
			}
			if err := c.Delete(ctx, member); err != nil {
				return err
			}
			if err := c.Create(ctx, replacementAnchor); err != nil {
				return err
			}
			if err := c.Create(ctx, replacementMember); err != nil {
				return err
			}
			podList := list.(*corev1.PodList)
			// Put the replacement member first to prove no page item is deleted
			// before the anchor is revalidated.
			podList.Items = []corev1.Pod{*replacementMember.DeepCopy(), *replacementAnchor.DeepCopy()}
			return nil
		},
	})
	localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
		Name:      groupName,
		Namespace: TestNamespace,
		Annotations: map[string]string{
			podconstants.IsGroupWorkloadAnnotationKey: podconstants.IsGroupWorkloadAnnotationValue,
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Pod",
			Name:       anchor.Name,
			UID:        "manager-anchor-uid",
			Controller: new(true),
		}},
	}}

	err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, utiltesting.NewClientBuilder().Build(), workerClient, &multiKueueAdapter{}, client.ObjectKeyFromObject(anchor), localWorkload, origin)
	if !errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
		t.Fatalf("cleanup error = %v, want %v", err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue)
	}
	for _, replacement := range []*corev1.Pod{replacementAnchor, replacementMember} {
		got := &corev1.Pod{}
		if err := workerClient.Get(ctx, client.ObjectKeyFromObject(replacement), got); err != nil {
			t.Fatalf("replacement Pod %q Get() error = %v", replacement.Name, err)
		}
		if got.UID != replacement.UID {
			t.Fatalf("replacement Pod %q UID = %q, want %q", replacement.Name, got.UID, replacement.UID)
		}
	}
}

func TestDeleteRemotePodUsesUIDPrecondition(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	key := types.NamespacedName{Name: "pod", Namespace: TestNamespace}
	original := utiltestingpod.MakePod(key.Name, key.Namespace).
		UID("original-uid").
		PrebuiltWorkloadLabel("workload").
		Label(kueue.MultiKueueOriginLabel, "origin").
		Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-pod-uid").
		Obj()
	replacement := original.DeepCopy()
	replacement.UID = "replacement-uid"

	baseClient := utiltesting.NewClientBuilder().WithObjects(original).Build()
	workerClient := interceptor.NewClient(baseClient, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deleteOptions := &client.DeleteOptions{}
			for _, opt := range opts {
				opt.ApplyToDelete(deleteOptions)
			}
			if deleteOptions.Preconditions == nil || deleteOptions.Preconditions.UID == nil || *deleteOptions.Preconditions.UID != original.UID {
				t.Errorf("Delete() UID precondition = %v, want %q", deleteOptions.Preconditions, original.UID)
			}
			if err := c.Delete(ctx, obj); err != nil {
				return err
			}
			if err := c.Create(ctx, replacement); err != nil {
				return err
			}
			return apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, key.Name, errors.New("UID precondition failed"))
		},
	})

	adapter := &multiKueueAdapter{}
	managerClient := utiltesting.NewClientBuilder().WithObjects(utiltestingpod.MakePod(key.Name, key.Namespace).UID("manager-pod-uid").Obj()).Build()
	err := adapter.DeleteRemoteObject(ctx, managerClient, workerClient, key)
	if !apierrors.IsConflict(err) {
		t.Fatalf("DeleteRemoteObject() error = %v, want conflict", err)
	}
	got := &corev1.Pod{}
	if err := workerClient.Get(ctx, key, got); err != nil {
		t.Fatalf("Get() replacement Pod: %v", err)
	}
	if got.UID != replacement.UID {
		t.Fatalf("replacement Pod UID = %q, want %q", got.UID, replacement.UID)
	}
}

func TestDeleteRemotePodRejectsReplacementBeforeAdapterCleanup(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	key := types.NamespacedName{Name: "pod", Namespace: TestNamespace}
	original := utiltestingpod.MakePod(key.Name, key.Namespace).
		UID("original-uid").
		PrebuiltWorkloadLabel("workload").
		Label(kueue.MultiKueueOriginLabel, "origin").
		Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-pod-uid").
		Obj()
	replacement := original.DeepCopy()
	replacement.UID = "replacement-uid"

	getCount := 0
	deleteCalled := false
	baseClient := utiltesting.NewClientBuilder().WithObjects(original).Build()
	workerClient := interceptor.NewClient(baseClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			getCount++
			if getCount == 2 {
				if err := c.Delete(ctx, original); err != nil {
					return err
				}
				if err := c.Create(ctx, replacement); err != nil {
					return err
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			deleteCalled = true
			return nil
		},
	})

	managerClient := utiltesting.NewClientBuilder().WithObjects(utiltestingpod.MakePod(key.Name, key.Namespace).UID("manager-pod-uid").Obj()).Build()
	adapter := &multiKueueAdapter{}
	err := adapter.DeleteRemoteObject(ctx, managerClient, workerClient, key)
	if !errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
		t.Fatalf("DeleteRemoteObject() error = %v, want %v", err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue)
	}
	if deleteCalled {
		t.Fatal("Delete() called for a replacement Pod")
	}
	got := &corev1.Pod{}
	if err := workerClient.Get(ctx, key, got); err != nil {
		t.Fatalf("Get() replacement Pod: %v", err)
	}
	if got.UID != replacement.UID {
		t.Fatalf("replacement Pod UID = %q, want %q", got.UID, replacement.UID)
	}
}
