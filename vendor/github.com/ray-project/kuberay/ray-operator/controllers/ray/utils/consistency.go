package utils

import (
	"reflect"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/pkg/features"
)

// Checks whether the old and new RayClusterStatus are inconsistent by comparing different fields. If the only
// differences between the old and new status are the `LastUpdateTime` and `ObservedGeneration` fields, the
// status update will not be triggered.
//
// TODO (kevin85421): The field `ObservedGeneration` is not being well-maintained at the moment. In the future,
// this field should be used to determine whether to update this CR or not.
func InconsistentRayClusterStatus(oldStatus rayv1.RayClusterStatus, newStatus rayv1.RayClusterStatus) bool {
	if oldStatus.State != newStatus.State || oldStatus.Reason != newStatus.Reason {
		return true
	}
	if oldStatus.ReadyWorkerReplicas != newStatus.ReadyWorkerReplicas ||
		oldStatus.AvailableWorkerReplicas != newStatus.AvailableWorkerReplicas ||
		oldStatus.DesiredWorkerReplicas != newStatus.DesiredWorkerReplicas ||
		oldStatus.MinWorkerReplicas != newStatus.MinWorkerReplicas ||
		oldStatus.MaxWorkerReplicas != newStatus.MaxWorkerReplicas {
		return true
	}
	if !reflect.DeepEqual(oldStatus.Endpoints, newStatus.Endpoints) || !reflect.DeepEqual(oldStatus.Head, newStatus.Head) {
		return true
	}
	if !reflect.DeepEqual(oldStatus.Conditions, newStatus.Conditions) {
		return true
	}
	return false
}

// Checks whether the old and new RayServiceStatus are inconsistent by comparing different fields.
// The RayClusterStatus field is only for observability in RayService CR, and changes to it will not trigger the status update.
func inconsistentRayServiceStatus(oldStatus rayv1.RayServiceStatus, newStatus rayv1.RayServiceStatus) bool {
	if oldStatus.RayClusterName != newStatus.RayClusterName {
		return true
	}

	if len(oldStatus.Applications) != len(newStatus.Applications) {
		return true
	}

	var ok bool
	for appName, newAppStatus := range newStatus.Applications {
		var oldAppStatus rayv1.AppStatus
		if oldAppStatus, ok = oldStatus.Applications[appName]; !ok {
			return true
		}

		if oldAppStatus.Status != newAppStatus.Status {
			return true
		} else if oldAppStatus.Message != newAppStatus.Message {
			return true
		}

		if len(oldAppStatus.Deployments) != len(newAppStatus.Deployments) {
			return true
		}

		for deploymentName, newDeploymentStatus := range newAppStatus.Deployments {
			var oldDeploymentStatus rayv1.ServeDeploymentStatus
			if oldDeploymentStatus, ok = oldAppStatus.Deployments[deploymentName]; !ok {
				return true
			}

			if oldDeploymentStatus.Status != newDeploymentStatus.Status {
				return true
			} else if oldDeploymentStatus.Message != newDeploymentStatus.Message {
				return true
			}
		}
	}

	if features.Enabled(features.RayServiceIncrementalUpgrade) {
		// Also check for changes in IncrementalUpgrade related Status fields.
		if oldStatus.TrafficRoutedPercent != newStatus.TrafficRoutedPercent ||
			oldStatus.TargetCapacity != newStatus.TargetCapacity ||
			oldStatus.LastTrafficMigratedTime != newStatus.LastTrafficMigratedTime {
			return true
		}
	}

	return false
}

// Determine whether to update the status of the RayService instance.
func InconsistentRayServiceStatuses(oldStatus rayv1.RayServiceStatuses, newStatus rayv1.RayServiceStatuses) bool {
	if oldStatus.ServiceStatus != newStatus.ServiceStatus {
		return true
	}

	if oldStatus.NumServeEndpoints != newStatus.NumServeEndpoints {
		return true
	}

	if !reflect.DeepEqual(oldStatus.Conditions, newStatus.Conditions) {
		return true
	}

	if inconsistentRayServiceStatus(oldStatus.ActiveServiceStatus, newStatus.ActiveServiceStatus) {
		return true
	}

	if inconsistentRayServiceStatus(oldStatus.PendingServiceStatus, newStatus.PendingServiceStatus) {
		return true
	}

	return false
}
