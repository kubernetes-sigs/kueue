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

package job

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// EnsureOneWorkload will query for the single matched workload corresponding to job and return it.
// If there're more than one workload, we should delete the excess ones.
// The returned workload could be nil.
func EnsureOneWorkload(ctx context.Context, cli client.Client, req ctrl.Request, record record.EventRecorder, job GenericJob) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	// Find a matching workload first if there is one.
	var toDelete []*kueue.Workload
	var match *kueue.Workload

	if pwName := job.ParentWorkloadName(); pwName != "" {
		pw := kueue.Workload{}
		NamespacedName := types.NamespacedName{
			Name:      pwName,
			Namespace: job.Object().GetNamespace(),
		}
		if err := cli.Get(ctx, NamespacedName, &pw); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			log.V(2).Info("job with no matching parent workload", "parent-workload", pwName)
		} else {
			match = &pw
		}
	}

	var workloads kueue.WorkloadList
	if err := cli.List(ctx, &workloads, client.InNamespace(job.Object().GetNamespace()),
		client.MatchingFields{ownerKey: job.Object().GetName()}); err != nil {
		log.Error(err, "Unable to list child workloads")
		return nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		owner := metav1.GetControllerOf(w)
		// Indexes don't work in unit tests, so we explicitly check for the
		// owner here.
		if owner.Name != job.Object().GetName() {
			continue
		}
		if match == nil && job.EquivalentToWorkload(*w) {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	// If there is no matching workload and the job is running, suspend it.
	if match == nil && !job.IsSuspend() {
		log.V(2).Info("job with no matching workload, suspending")
		var w *kueue.Workload
		if len(workloads.Items) == 1 {
			// The job may have been modified and hence the existing workload
			// doesn't match the job anymore. All bets are off if there are more
			// than one workload...
			w = &workloads.Items[0]
		}
		if err := StopJob(ctx, cli, record, job, w, "No matching Workload"); err != nil {
			log.Error(err, "stopping job")
		}
	}

	// Delete duplicate workload instances.
	existedWls := 0
	for i := range toDelete {
		err := cli.Delete(ctx, toDelete[i])
		if err == nil || !apierrors.IsNotFound(err) {
			existedWls++
		}
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete workload")
		}
		if err == nil {
			record.Eventf(job.Object(), corev1.EventTypeNormal, "DeletedWorkload",
				"Deleted not matching Workload: %v", workload.Key(toDelete[i]))
		}
	}

	if existedWls != 0 {
		if match == nil {
			return nil, fmt.Errorf("no matching workload was found, tried deleting %d existing workload(s)", existedWls)
		}
		return nil, fmt.Errorf("only one workload should exist, found %d", len(workloads.Items))
	}

	return match, nil
}

// StartJob will unsuspend the job, and also inject the node affinity.
func StartJob(ctx context.Context, client client.Client, record record.EventRecorder, job GenericJob, wl *kueue.Workload) error {
	nodeSelectors, err := GetNodeSelectors(ctx, client, wl)
	if err != nil {
		return err
	}

	if err := job.InjectNodeAffinity(nodeSelectors); err != nil {
		return err
	}

	if err := job.UnSuspend(); err != nil {
		return err
	}

	if err := client.Update(ctx, job.Object()); err != nil {
		return err
	}

	record.Eventf(job.Object(), corev1.EventTypeNormal, "Started",
		"Admitted by clusterQueue %v", wl.Status.Admission.ClusterQueue)

	return nil
}

// StopJob will suspend the job, and also restore node affinity, reset job status if needed.
func StopJob(ctx context.Context, client client.Client, record record.EventRecorder, job GenericJob, wl *kueue.Workload, eventMsg string) error {
	// Suspend the job at first then we're able to update the scheduling directives.
	if err := job.Suspend(); err != nil {
		return err
	}

	if err := client.Update(ctx, job.Object()); err != nil {
		return err
	}

	record.Eventf(job.Object(), corev1.EventTypeNormal, "Stopped", eventMsg)

	if job.ResetStatus() {
		if err := client.Status().Update(ctx, job.Object()); err != nil {
			return err
		}
	}

	if wl != nil {
		if err := job.RestoreNodeAffinity([]map[string]string{wl.Spec.PodSets[0].Template.Spec.NodeSelector}); err != nil {
			return err
		}
		return client.Update(ctx, job.Object())
	}

	return nil
}

// CreateWorkload will create a workload from the corresponding job.
func CreateWorkload(ctx context.Context, client client.Client, scheme *runtime.Scheme, job GenericJob) (*kueue.Workload, error) {
	wl, err := ConstructWorkload(ctx, client, scheme, job)
	if err != nil {
		return nil, err
	}

	if err = client.Create(ctx, wl); err != nil {
		return nil, err
	}

	return wl, nil
}

// ConstructWorkload will derive a workload from the corresponding job.
func ConstructWorkload(ctx context.Context, client client.Client, scheme *runtime.Scheme, job GenericJob) (*kueue.Workload, error) {
	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetWorkloadNameForJob(job.Object().GetName()),
			Namespace: job.Object().GetNamespace(),
		},
		Spec: kueue.WorkloadSpec{
			PodSets:   job.PodSets(),
			QueueName: job.QueueName(),
		},
	}

	priorityClassName, p, err := utilpriority.GetPriorityFromPriorityClass(
		ctx, client, job.PriorityClass())
	if err != nil {
		return nil, err
	}

	wl.Spec.PriorityClassName = priorityClassName
	wl.Spec.Priority = &p

	if err := ctrl.SetControllerReference(job.Object(), wl, scheme); err != nil {
		return nil, err
	}
	return wl, nil
}

// GetNodeSelectors will extract node selectors from admitted workloads.
func GetNodeSelectors(ctx context.Context, client client.Client, w *kueue.Workload) ([]map[string]string, error) {
	if len(w.Status.Admission.PodSetFlavors) == 0 {
		return nil, nil
	}

	nodeSelectors := make([]map[string]string, len(w.Status.Admission.PodSetFlavors))

	for i, podSetFlavor := range w.Status.Admission.PodSetFlavors {
		processedFlvs := sets.NewString()
		nodeSelector := map[string]string{}
		for _, flvRef := range podSetFlavor.Flavors {
			flvName := string(flvRef)
			if processedFlvs.Has(flvName) {
				continue
			}
			// Lookup the ResourceFlavors to fetch the node affinity labels to apply on the job.
			flv := kueue.ResourceFlavor{}
			if err := client.Get(ctx, types.NamespacedName{Name: string(flvName)}, &flv); err != nil {
				return nil, err
			}
			for k, v := range flv.Spec.NodeLabels {
				nodeSelector[k] = v
			}
			processedFlvs.Insert(flvName)
		}

		nodeSelectors[i] = nodeSelector
	}
	return nodeSelectors, nil
}

// UpdateQueueNameIfChanged will update workload queue name if changed.
func UpdateQueueNameIfChanged(ctx context.Context, client client.Client, job GenericJob, wl *kueue.Workload) error {
	queueName := job.QueueName()
	if wl.Spec.QueueName != queueName {
		wl.Spec.QueueName = queueName
		return client.Update(ctx, wl)
	}
	return nil
}

// SetWorkloadCondition will update the workload condition by the provide one.
func SetWorkloadCondition(ctx context.Context, client client.Client, wl *kueue.Workload, condition metav1.Condition) error {
	apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
	return client.Status().Update(ctx, wl)
}
