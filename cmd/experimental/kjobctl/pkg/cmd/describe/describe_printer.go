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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	describehelper "k8s.io/kubectl/pkg/describe"
)

// ResourceDescriber generates output for the named resource or an error
// if the output could not be generated. Implementers typically
// abstract the retrieval of the named object from a remote server.
type ResourceDescriber interface {
	Describe(object *unstructured.Unstructured) (output string, err error)
}

// Describer returns a Describer for displaying the specified RESTMapping type or an error.
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
		{Group: batchv1.GroupName, Kind: "Job"}: &JobDescriber{},
		{Group: corev1.GroupName, Kind: "Pod"}:  &PodDescriber{},
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
