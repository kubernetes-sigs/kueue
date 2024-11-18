/*
Copyright 2024 The Kubernetes Authors.

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

package describe

import (
	"fmt"
	"io"
	"strconv"
	"time"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	describehelper "k8s.io/kubectl/pkg/describe"
	"k8s.io/utils/ptr"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
)

// ResourceDescriber generates output for the named resource or an error
// if the output could not be generated. Implementers typically
// abstract the retrieval of the named object from a remote server.
type ResourceDescriber interface {
	Describe(object *unstructured.Unstructured) (output string, err error)
}

// NewResourceDescriber returns a Describer for displaying the specified RESTMapping type or an error.
func NewResourceDescriber(mapping *meta.RESTMapping) (ResourceDescriber, error) {
	if describer, ok := DescriberFor(mapping.GroupVersionKind.GroupKind()); ok {
		return describer, nil
	}

	return nil, fmt.Errorf("no description has been implemented for %s", mapping.GroupVersionKind.String())
}

// DescriberFor returns the default describe functions for each of the standard
// Kubernetes types.
func DescriberFor(kind schema.GroupKind) (ResourceDescriber, bool) {
	describers := map[schema.GroupKind]ResourceDescriber{
		{Group: batchv1.GroupName, Kind: "Job"}:               &JobDescriber{},
		{Group: corev1.GroupName, Kind: "Pod"}:                &PodDescriber{},
		{Group: rayv1.GroupVersion.Group, Kind: "RayJob"}:     &RayJobDescriber{},
		{Group: rayv1.GroupVersion.Group, Kind: "RayCluster"}: &RayClusterDescriber{},
		{Group: corev1.GroupName, Kind: "ConfigMap"}:          &ConfigMapDescriber{},
	}

	f, ok := describers[kind]
	return f, ok
}

// JobDescriber generates information about a job and the pods it has created.
type JobDescriber struct{}

func (d *JobDescriber) Describe(object *unstructured.Unstructured) (string, error) {
	job := &batchv1.Job{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), job)
	if err != nil {
		return "", err
	}

	return describeJob(job)
}

func describeJob(job *batchv1.Job) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describehelper.NewPrefixWriter(out)

		w.Write(IndentLevelZero, "Name:\t%s\n", job.Name)
		w.Write(IndentLevelZero, "Namespace:\t%s\n", job.Namespace)
		printLabelsMultiline(w, "Labels", job.Labels)
		printLabelsMultiline(w, "Annotations", job.Annotations)
		if job.Spec.Parallelism != nil {
			w.Write(IndentLevelZero, "Parallelism:\t%d\n", *job.Spec.Parallelism)
		}
		if job.Spec.Completions != nil {
			w.Write(IndentLevelZero, "Completions:\t%d\n", *job.Spec.Completions)
		} else {
			w.Write(IndentLevelZero, "Completions:\t<unset>\n")
		}
		if job.Status.StartTime != nil {
			w.Write(IndentLevelZero, "Start Time:\t%s\n", job.Status.StartTime.Time.Format(time.RFC1123Z))
		}
		if job.Status.CompletionTime != nil {
			w.Write(IndentLevelZero, "Completed At:\t%s\n", job.Status.CompletionTime.Time.Format(time.RFC1123Z))
		}
		if job.Status.StartTime != nil && job.Status.CompletionTime != nil {
			w.Write(IndentLevelZero, "Duration:\t%s\n", duration.HumanDuration(job.Status.CompletionTime.Sub(job.Status.StartTime.Time)))
		}
		if job.Status.Ready == nil {
			w.Write(IndentLevelZero, "Pods Statuses:\t%d Active / %d Succeeded / %d Failed\n", job.Status.Active, job.Status.Succeeded, job.Status.Failed)
		} else {
			w.Write(IndentLevelZero, "Pods Statuses:\t%d Active (%d Ready) / %d Succeeded / %d Failed\n", job.Status.Active, *job.Status.Ready, job.Status.Succeeded, job.Status.Failed)
		}

		describePodTemplate(&job.Spec.Template, w)

		return nil
	})
}

// PodDescriber generates information about a pod.
type PodDescriber struct{}

func (d *PodDescriber) Describe(object *unstructured.Unstructured) (string, error) {
	pod := &corev1.Pod{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), pod)
	if err != nil {
		return "", err
	}

	return describePod(pod)
}

func describePod(pod *corev1.Pod) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describehelper.NewPrefixWriter(out)

		w.Write(IndentLevelZero, "Name:\t%s\n", pod.Name)
		w.Write(IndentLevelZero, "Namespace:\t%s\n", pod.Namespace)
		if pod.Status.StartTime != nil {
			w.Write(IndentLevelZero, "Start Time:\t%s\n", pod.Status.StartTime.Time.Format(time.RFC1123Z))
		}
		printLabelsMultiline(w, "Labels", pod.Labels)

		if pod.DeletionTimestamp != nil && pod.Status.Phase != corev1.PodFailed && pod.Status.Phase != corev1.PodSucceeded {
			w.Write(IndentLevelZero, "Status:\tTerminating (lasts %s)\n", translateTimestampSince(*pod.DeletionTimestamp))
			w.Write(IndentLevelZero, "Termination Grace Period:\t%ds\n", *pod.DeletionGracePeriodSeconds)
		} else {
			w.Write(IndentLevelZero, "Status:\t%s\n", string(pod.Status.Phase))
		}
		if len(pod.Status.Reason) > 0 {
			w.Write(IndentLevelZero, "Reason:\t%s\n", pod.Status.Reason)
		}
		if len(pod.Status.Message) > 0 {
			w.Write(IndentLevelZero, "Message:\t%s\n", pod.Status.Message)
		}

		if len(pod.Spec.InitContainers) > 0 {
			describeContainers("Init Containers", pod.Spec.InitContainers, w, "")
		}
		describeContainers("Containers", pod.Spec.Containers, w, "")
		if len(pod.Spec.EphemeralContainers) > 0 {
			var ec []corev1.Container
			for i := range pod.Spec.EphemeralContainers {
				ec = append(ec, corev1.Container(pod.Spec.EphemeralContainers[i].EphemeralContainerCommon))
			}
			describeContainers("Ephemeral Containers", ec, w, "")
		}

		describeVolumes(pod.Spec.Volumes, w, "")

		return nil
	})
}

// RayJobDescriber generates information about a ray job.
type RayJobDescriber struct{}

func (d *RayJobDescriber) Describe(object *unstructured.Unstructured) (string, error) {
	rayJob := &rayv1.RayJob{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), rayJob)
	if err != nil {
		return "", err
	}

	return describeRayJob(rayJob)
}

func describeRayJob(rayJob *rayv1.RayJob) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describehelper.NewPrefixWriter(out)

		w.Write(IndentLevelZero, "Name:\t%s\n", rayJob.Name)
		w.Write(IndentLevelZero, "Namespace:\t%s\n", rayJob.Namespace)
		if rayJob.Status.StartTime != nil {
			w.Write(IndentLevelZero, "Start Time:\t%s\n", rayJob.Status.StartTime.Format(time.RFC1123Z))
		}
		if rayJob.Status.EndTime != nil {
			w.Write(IndentLevelZero, "End Time:\t%s\n", rayJob.Status.EndTime.Format(time.RFC1123Z))
		}
		printLabelsMultiline(w, "Labels", rayJob.Labels)

		if rayJob.DeletionTimestamp != nil {
			w.Write(IndentLevelZero, "Job Deployment Status:\tTerminating (lasts %s)\n", translateTimestampSince(*rayJob.DeletionTimestamp))
			w.Write(IndentLevelZero, "Job Status:\tTerminating (lasts %s)\n", translateTimestampSince(*rayJob.DeletionTimestamp))
			w.Write(IndentLevelZero, "Termination Grace Period:\t%ds\n", *rayJob.DeletionGracePeriodSeconds)
		} else {
			if len(rayJob.Status.JobDeploymentStatus) > 0 {
				w.Write(IndentLevelZero, "Job Deployment Status:\t%s\n", string(rayJob.Status.JobDeploymentStatus))
			}
			if len(rayJob.Status.JobStatus) > 0 {
				w.Write(IndentLevelZero, "Job Status:\t%s\n", string(rayJob.Status.JobStatus))
			}
			if len(rayJob.Status.Reason) > 0 {
				w.Write(IndentLevelZero, "Reason:\t%s\n", rayJob.Status.Reason)
			}
			if len(rayJob.Status.Message) > 0 {
				w.Write(IndentLevelZero, "Message:\t%s\n", rayJob.Status.Message)
			}
		}

		if len(rayJob.Status.RayClusterName) > 0 {
			w.Write(IndentLevelZero, "Ray Cluster Name:\t%s\n", rayJob.Status.RayClusterName)
		}

		w.Write(IndentLevelZero, "Ray Cluster Status:\n")
		w.Write(IndentLevelOne, "Desired CPU:\t%s\n", rayJob.Status.RayClusterStatus.DesiredCPU.String())
		w.Write(IndentLevelOne, "Desired GPU:\t%s\n", rayJob.Status.RayClusterStatus.DesiredGPU.String())
		w.Write(IndentLevelOne, "Desired Memory:\t%s\n", rayJob.Status.RayClusterStatus.DesiredMemory.String())
		w.Write(IndentLevelOne, "Desired TPU:\t%s\n", rayJob.Status.RayClusterStatus.DesiredTPU.String())

		return nil
	})
}

// RayClusterDescriber generates information about a ray cluster.
type RayClusterDescriber struct{}

func (d *RayClusterDescriber) Describe(object *unstructured.Unstructured) (string, error) {
	rayCluster := &rayv1.RayCluster{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), rayCluster)
	if err != nil {
		return "", err
	}

	return describeRayCluster(rayCluster)
}

func describeRayCluster(rayCluster *rayv1.RayCluster) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describehelper.NewPrefixWriter(out)

		w.Write(IndentLevelZero, "Name:\t%s\n", rayCluster.Name)
		w.Write(IndentLevelZero, "Namespace:\t%s\n", rayCluster.Namespace)

		printLabelsMultiline(w, "Labels", rayCluster.Labels)

		w.Write(IndentLevelZero, "Suspend:\t%t\n", ptr.Deref(rayCluster.Spec.Suspend, false))
		w.Write(IndentLevelZero, "State:\t%s\n", string(rayCluster.Status.State))
		if len(rayCluster.Status.Reason) > 0 {
			w.Write(IndentLevelZero, "Reason:\t%s\n", rayCluster.Status.Reason)
		}

		w.Write(IndentLevelZero, "Desired CPU:\t%s\n", rayCluster.Status.DesiredCPU.String())
		w.Write(IndentLevelZero, "Desired GPU:\t%s\n", rayCluster.Status.DesiredGPU.String())
		w.Write(IndentLevelZero, "Desired Memory:\t%s\n", rayCluster.Status.DesiredMemory.String())
		w.Write(IndentLevelZero, "Desired TPU:\t%s\n", rayCluster.Status.DesiredTPU.String())

		w.Write(IndentLevelZero, "Ready Worker Replicas:\t%d\n", rayCluster.Status.ReadyWorkerReplicas)
		w.Write(IndentLevelZero, "Available Worker Replicas:\t%d\n", rayCluster.Status.AvailableWorkerReplicas)
		w.Write(IndentLevelZero, "Desired Worker Replicas:\t%d\n", rayCluster.Status.DesiredWorkerReplicas)
		w.Write(IndentLevelZero, "Min Worker Replicas:\t%d\n", rayCluster.Status.MinWorkerReplicas)
		w.Write(IndentLevelZero, "Max Worker Replicas:\t%d\n", rayCluster.Status.MaxWorkerReplicas)

		w.Write(IndentLevelZero, "Head Group:\n")
		headGroupWriter := describehelper.NewNestedPrefixWriter(w, 1)
		printLabelsMultiline(headGroupWriter, "Start Params", rayCluster.Spec.HeadGroupSpec.RayStartParams)
		describePodTemplate(&rayCluster.Spec.HeadGroupSpec.Template, headGroupWriter)

		w.Write(IndentLevelZero, "Worker Groups:\n")
		for _, wg := range rayCluster.Spec.WorkerGroupSpecs {
			w.Write(IndentLevelOne, fmt.Sprintf("%s:\n", wg.GroupName))
			workerGroupWriter := describehelper.NewNestedPrefixWriter(w, 2)
			if wg.Replicas != nil {
				workerGroupWriter.Write(IndentLevelZero, "Replicas:\t%d\n", *wg.Replicas)
			}
			if wg.MinReplicas != nil {
				workerGroupWriter.Write(IndentLevelZero, "Min Replicas:\t%d\n", *wg.MinReplicas)
			}
			if wg.MaxReplicas != nil {
				workerGroupWriter.Write(IndentLevelZero, "Max Replicas:\t%d\n", *wg.MaxReplicas)
			}
			printLabelsMultiline(workerGroupWriter, "Start Params", wg.RayStartParams)
			describePodTemplate(&wg.Template, workerGroupWriter)
		}

		return nil
	})
}

// ConfigMapDescriber generates information about a configMap.
type ConfigMapDescriber struct{}

func (d *ConfigMapDescriber) Describe(object *unstructured.Unstructured) (string, error) {
	configMap := &corev1.ConfigMap{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), configMap)
	if err != nil {
		return "", err
	}

	return describeConfigMap(configMap)
}

func describeConfigMap(configMap *corev1.ConfigMap) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describehelper.NewPrefixWriter(out)

		w.Write(IndentLevelZero, "Name:\t%s\n", configMap.Name)
		w.Write(IndentLevelZero, "Namespace:\t%s\n", configMap.Namespace)
		printLabelsMultiline(w, "Labels", configMap.Labels)

		w.Write(IndentLevelZero, "\nData\n====\n")
		for _, k := range utilmaps.SortedKeys(configMap.Data) {
			w.Write(IndentLevelZero, "%s:\n----\n", k)
			w.Write(IndentLevelZero, "%s\n", configMap.Data[k])
			w.Write(IndentLevelZero, "\n")
		}

		w.Write(IndentLevelZero, "\nBinaryData\n====\n")
		for _, k := range utilmaps.SortedKeys(configMap.BinaryData) {
			w.Write(IndentLevelZero, "%s: %s bytes\n", k, strconv.Itoa(len(configMap.BinaryData[k])))
		}
		w.Write(IndentLevelZero, "\n")

		return nil
	})
}
