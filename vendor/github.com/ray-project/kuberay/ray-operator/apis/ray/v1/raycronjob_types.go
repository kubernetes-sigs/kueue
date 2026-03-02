package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

//nolint:govet // RayCronJobSpec defines the desired state of RayCronJob
type RayCronJobSpec struct {
	// JobTemplate defines the job spec that will be created by cron scheduling
	JobTemplate RayJobSpec `json:"jobTemplate"`
	// Schedule is the cron schedule string
	Schedule string `json:"schedule"`
	// Suspend tells the controller to suspend the scheduling, it does not apply to
	// scheduled RayJob.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// RayCronJobStatus defines the observed state of RayCronJob
type RayCronJobStatus struct {
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="schedule",type=string,JSONPath=".spec.schedule",priority=0
//+kubebuilder:printcolumn:name="last schedule",type=string,JSONPath=".status.lastScheduleTime",priority=0
//+kubebuilder:printcolumn:name="age",type="date",JSONPath=".metadata.creationTimestamp",priority=0
//+kubebuilder:printcolumn:name="suspend",type=boolean,JSONPath=".spec.suspend",priority=0

// +genclient
// +kubebuilder:resource:categories=all
// +kubebuilder:storageversion
//
//nolint:govet // RayCronJob is the Schema for the raycronjobs API
type RayCronJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RayCronJobSpec   `json:"spec,omitempty"`
	Status            RayCronJobStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RayCronJobList contains a list of RayCronJob
type RayCronJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RayCronJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RayCronJob{}, &RayCronJobList{})
}
