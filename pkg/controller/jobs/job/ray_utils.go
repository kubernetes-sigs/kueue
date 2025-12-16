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

package job

import (
	"context"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func isRaySubmitterJobWithAutoScaling(ctx context.Context, jobObj client.Object, k8sClient client.Client) (bool, *rayv1.RayJob, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("jobName", jobObj.GetName(), "jobNamespace", jobObj.GetNamespace())

	owner := metav1.GetControllerOf(jobObj)
	if owner == nil {
		log.V(5).Info("Did not find owner for job")
		return false, nil, nil
	}

	createdByRayJob := owner.APIVersion == rayv1.GroupVersion.String() && owner.Kind == "RayJob"
	if !createdByRayJob {
		log.V(5).Info("Owner object for job is not RayJob", "ownerKind", owner.Kind, "ownerName", owner.Name)
		return false, nil, nil
	}

	parentObj := jobframework.GetEmptyOwnerObject(owner)
	if parentObj == nil {
		log.V(5).Info("Did not get empty owner object for job", "ownerKind", owner.Kind, "ownerName", owner.Name)
		return false, nil, nil
	}

	err := k8sClient.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: jobObj.GetNamespace()}, parentObj)
	if err != nil {
		log.Error(err, "Failed to get owner object from k8s", "ownerKind", owner.Kind, "ownerName", owner.Name)
		return false, nil, err
	}
	log.V(5).Info("Got owner object for job", "ownerKind", owner.Kind, "ownerName", owner.Name)

	rayJob, ok := parentObj.(*rayv1.RayJob)
	if !ok {
		log.V(5).Info("Owner object cannot be converted to RayJob", "ownerKind", owner.Kind, "ownerName", owner.Name)
		return false, nil, nil
	}

	if rayJob.Spec.RayClusterSpec == nil ||
		!ptr.Deref(rayJob.Spec.RayClusterSpec.EnableInTreeAutoscaling, false) {
		return false, nil, nil
	}

	return true, rayJob, nil
}

// copyRaySubmitterJobMetadata checks whether the job is Ray submitter job, if it is, copy queue label
// and workload slicing annotation from the owner RayJob to the submitter job.
func copyRaySubmitterJobMetadata(ctx context.Context, jobObj client.Object, k8sClient client.Client) error {
	if jobframework.QueueNameForObject(jobObj) != "" {
		return nil
	}

	// Copy label and annotation for Ray submitter job with autoscaling
	// See https://github.com/kubernetes-sigs/kueue/pull/8082
	isRaySubmitterJob, parentObj, err := isRaySubmitterJobWithAutoScaling(ctx, jobObj, k8sClient)
	if err != nil {
		return err
	}
	if !isRaySubmitterJob {
		return nil
	}

	queueName := parentObj.GetLabels()[constants.QueueLabel]
	if queueName != "" {
		jobLabels := jobObj.GetLabels()
		if jobLabels == nil {
			jobLabels = make(map[string]string, 1)
		}
		jobLabels[constants.QueueLabel] = queueName
		jobObj.SetLabels(jobLabels)
	}

	workloadslicingAnnotationValue := parentObj.GetAnnotations()[workloadslicing.EnabledAnnotationKey]
	if workloadslicingAnnotationValue != "" {
		jobAnnotations := jobObj.GetAnnotations()
		if jobAnnotations == nil {
			jobAnnotations = make(map[string]string, 1)
		}
		jobAnnotations[workloadslicing.EnabledAnnotationKey] = workloadslicingAnnotationValue
		jobObj.SetAnnotations(jobAnnotations)
	}
	return nil
}
