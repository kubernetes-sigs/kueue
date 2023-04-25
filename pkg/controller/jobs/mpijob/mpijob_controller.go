/*
Copyright 2023 The Kubernetes Authors.

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
	"context"
	"strings"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	gvk = kubeflow.SchemeGroupVersionKind

	FrameworkName = "kubeflow.org/mpijob"
)

// MPIJobReconciler reconciles a Job object
type MPIJobReconciler jobframework.JobReconciler

func NewReconciler(
	scheme *runtime.Scheme,
	client client.Client,
	record record.EventRecorder,
	opts ...jobframework.Option) *MPIJobReconciler {
	return (*MPIJobReconciler)(jobframework.NewReconciler(scheme,
		client,
		record,
		opts...,
	))
}

type MPIJob struct {
	kubeflow.MPIJob
}

func (j *MPIJob) Object() client.Object {
	return &j.MPIJob
}

func (j *MPIJob) IsSuspended() bool {
	return j.Spec.RunPolicy.Suspend != nil && *j.Spec.RunPolicy.Suspend
}

func (j *MPIJob) IsActive() bool {
	for _, replicaStatus := range j.Status.ReplicaStatuses {
		if replicaStatus.Active != 0 {
			return true
		}
	}
	return false
}

func (j *MPIJob) Suspend() {
	j.Spec.RunPolicy.Suspend = pointer.Bool(true)
}

func (j *MPIJob) ResetStatus() bool {
	if j.Status.StartTime == nil {
		return false
	}
	j.Status.StartTime = nil
	return true
}

func (j *MPIJob) GetGVK() schema.GroupVersionKind {
	return gvk
}

func (j *MPIJob) PodSets() []kueue.PodSet {
	replicaTypes := orderedReplicaTypes(&j.Spec)
	podSets := make([]kueue.PodSet, len(replicaTypes))
	for index, mpiReplicaType := range replicaTypes {
		podSets[index] = kueue.PodSet{
			Name:     strings.ToLower(string(mpiReplicaType)),
			Template: *j.Spec.MPIReplicaSpecs[mpiReplicaType].Template.DeepCopy(),
			Count:    podsCount(&j.Spec, mpiReplicaType),
		}
	}
	return podSets
}

func (j *MPIJob) RunWithNodeAffinity(nodeSelectors []jobframework.PodSetNodeSelector) {
	j.Spec.RunPolicy.Suspend = pointer.Bool(false)
	if len(nodeSelectors) == 0 {
		return
	}
	// The node selectors are provided in the same order as the generated list of
	// podSets, use the same ordering logic to restore them.
	orderedReplicaTypes := orderedReplicaTypes(&j.Spec)
	for index := range nodeSelectors {
		replicaType := orderedReplicaTypes[index]
		nodeSelector := nodeSelectors[index]
		if len(nodeSelector.NodeSelector) != 0 {
			if j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector == nil {
				j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector = nodeSelector.NodeSelector
			} else {
				for k, v := range nodeSelector.NodeSelector {
					j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector[k] = v
				}
			}
		}
	}
}

func (j *MPIJob) RestoreNodeAffinity(nodeSelectors []jobframework.PodSetNodeSelector) {
	orderedReplicaTypes := orderedReplicaTypes(&j.Spec)
	for index, nodeSelector := range nodeSelectors {
		replicaType := orderedReplicaTypes[index]
		if !equality.Semantic.DeepEqual(j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector, nodeSelector) {
			j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector = map[string]string{}
			for k, v := range nodeSelector.NodeSelector {
				j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector[k] = v
			}
		}
	}
}

func (j *MPIJob) Finished() (metav1.Condition, bool) {
	var conditionType kubeflow.JobConditionType
	var finished bool
	for _, c := range j.Status.Conditions {
		if (c.Type == kubeflow.JobSucceeded || c.Type == kubeflow.JobFailed) && c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			finished = true
			break
		}
	}

	message := "Job finished successfully"
	if conditionType == kubeflow.JobFailed {
		message = "Job failed"
	}
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: message,
	}
	return condition, finished
}

func (j *MPIJob) EquivalentToWorkload(wl kueue.Workload) bool {
	if len(wl.Spec.PodSets) != len(j.Spec.MPIReplicaSpecs) {
		return false
	}
	for index, mpiReplicaType := range orderedReplicaTypes(&j.Spec) {
		mpiReplicaSpec := j.Spec.MPIReplicaSpecs[mpiReplicaType]
		if pointer.Int32Deref(mpiReplicaSpec.Replicas, 1) != wl.Spec.PodSets[index].Count {
			return false
		}
		// nodeSelector may change, hence we are not checking for
		// equality of the whole j.Spec.Template.Spec.
		if !equality.Semantic.DeepEqual(mpiReplicaSpec.Template.Spec.InitContainers,
			wl.Spec.PodSets[index].Template.Spec.InitContainers) {
			return false
		}
		if !equality.Semantic.DeepEqual(mpiReplicaSpec.Template.Spec.Containers,
			wl.Spec.PodSets[index].Template.Spec.Containers) {
			return false
		}
	}
	return true
}

// PriorityClass calculates the priorityClass name needed for workload according to the following priorities:
//  1. .spec.runPolicy.schedulingPolicy.priorityClass
//  2. .spec.mpiReplicaSpecs[Launcher].template.spec.priorityClassName
//  3. .spec.mpiReplicaSpecs[Worker].template.spec.priorityClassName
//
// This function is inspired by an analogous one in mpi-controller:
// https://github.com/kubeflow/mpi-operator/blob/5946ef4157599a474ab82ff80e780d5c2546c9ee/pkg/controller/podgroup.go#L69-L72
func (j *MPIJob) PriorityClass() string {
	if j.Spec.RunPolicy.SchedulingPolicy != nil && len(j.Spec.RunPolicy.SchedulingPolicy.PriorityClass) != 0 {
		return j.Spec.RunPolicy.SchedulingPolicy.PriorityClass
	} else if l := j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; l != nil && len(l.Template.Spec.PriorityClassName) != 0 {
		return l.Template.Spec.PriorityClassName
	} else if w := j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; w != nil && len(w.Template.Spec.PriorityClassName) != 0 {
		return w.Template.Spec.PriorityClassName
	}
	return ""
}

func (j *MPIJob) PodsReady() bool {
	for _, c := range j.Status.Conditions {
		if c.Type == kubeflow.JobRunning && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager. It indexes workloads
// based on the owning jobs.
func (r *MPIJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeflow.MPIJob{}).
		Owns(&kueue.Workload{}).
		Complete(r)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

//+kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
//+kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs/status,verbs=get
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func (r *MPIJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fjr := (*jobframework.JobReconciler)(r)
	return fjr.ReconcileGenericJob(ctx, req, &MPIJob{})
}

func orderedReplicaTypes(jobSpec *kubeflow.MPIJobSpec) []kubeflow.MPIReplicaType {
	var result []kubeflow.MPIReplicaType
	if _, ok := jobSpec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; ok {
		result = append(result, kubeflow.MPIReplicaTypeLauncher)
	}
	if _, ok := jobSpec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; ok {
		result = append(result, kubeflow.MPIReplicaTypeWorker)
	}
	return result
}

func podsCount(jobSpec *kubeflow.MPIJobSpec, mpiReplicaType kubeflow.MPIReplicaType) int32 {
	return pointer.Int32Deref(jobSpec.MPIReplicaSpecs[mpiReplicaType].Replicas, 1)
}

func GetWorkloadNameForMPIJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
