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
	"encoding/json"
	"fmt"
	"net/http"

	jsonpatchapply "github.com/evanphx/json-patch/v5"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	// GangDefaultedAnnotation marks a Job whose spec.scheduling Kueue set. It
	// outlives the field, which the API server drops when WorkloadWithJob is off.
	GangDefaultedAnnotation = "kueue.x-k8s.io/gang-defaulted"

	ReasonGangSchedulingDefaulted      = "GangSchedulingDefaulted"
	ReasonGangSchedulingDefaultDropped = "GangSchedulingDefaultDropped"
)

// minKubeVersionForJobScheduling is the first release that serves
// batch/v1 Job.spec.scheduling. Compared on major and minor only: a
// distribution's 1.37.0-eks-... sorts below 1.37.0 under semver.
var minKubeVersionForJobScheduling = versionutil.MajorMinor(1, 37)

func gangSchedulingDefault() map[string]any {
	return map[string]any{
		"schedulingPolicy": map[string]any{"gang": map[string]any{}},
		"disruptionMode":   map[string]any{"all": map[string]any{}},
	}
}

type serverVersionGetter interface {
	GetServerVersion() versionutil.Version
}

// gangDefaultingHandler wraps the typed Job defaulter, which cannot emit
// spec.scheduling: the vendored batch/v1 Job has no such field, so it is lost
// at decode. This works on the request JSON after the typed patches, so the
// rules see the LocalQueue and suspend defaults.
type gangDefaultingHandler struct {
	typed   admission.Handler
	webhook *JobWebhook
}

func (h *gangDefaultingHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	resp := h.typed.Handle(ctx, req)
	if !resp.Allowed || req.Operation != admissionv1.Create || !features.Enabled(features.BatchJobGangSchedulingByDefault) {
		return resp
	}
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")

	defaulted, err := applyJSONPatches(req.Object.Raw, resp.Patches)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	mutated, reason, err := h.webhook.defaultGangScheduling(ctx, defaulted)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if mutated == nil {
		log.V(3).Info("Skipping the gang scheduling default", "reason", reason)
		return resp
	}
	patches, err := jsonpatch.CreatePatch(req.Object.Raw, mutated)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	log.V(3).Info("Applied the gang scheduling default")
	resp.Patches = patches
	return resp
}

func applyJSONPatches(raw []byte, ops []jsonpatch.Operation) ([]byte, error) {
	if len(ops) == 0 {
		return raw, nil
	}
	encoded, err := json.Marshal(ops)
	if err != nil {
		return nil, err
	}
	patch, err := jsonpatchapply.DecodePatch(encoded)
	if err != nil {
		return nil, err
	}
	return patch.Apply(raw)
}

// defaultGangScheduling returns the Job JSON with the gang scheduling default
// applied, or nil and the reason the default does not apply.
func (w *JobWebhook) defaultGangScheduling(ctx context.Context, raw []byte) ([]byte, string, error) {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(raw); err != nil {
		return nil, "", err
	}
	job := &batchv1.Job{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, job); err != nil {
		return nil, "", err
	}
	if reason := gangDefaultSkipReason(job, obj.Object, w.serverVersion()); reason != "" {
		return nil, reason, nil
	}
	managed, err := w.integrationManager.WorkloadShouldBeSuspended(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector)
	if err != nil {
		return nil, "", err
	}
	if !managed {
		return nil, "the Job is not managed by Kueue", nil
	}

	if err := unstructured.SetNestedMap(obj.Object, gangSchedulingDefault(), "spec", "scheduling"); err != nil {
		return nil, "", err
	}
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[GangDefaultedAnnotation] = "true"
	obj.SetAnnotations(annotations)
	mutated, err := obj.MarshalJSON()
	if err != nil {
		return nil, "", err
	}
	return mutated, "", nil
}

// gangDefaultSkipReason evaluates the rules that need no cluster state; the
// managed-by-Kueue rule is evaluated by the caller. obj is the Job as JSON,
// which is the only place spec.scheduling is visible.
func gangDefaultSkipReason(job *batchv1.Job, obj map[string]any, serverVersion *versionutil.Version) string {
	if serverVersion == nil {
		return "the API server version is unknown"
	}
	if serverVersion.LessThan(minKubeVersionForJobScheduling) {
		return fmt.Sprintf("the API server version %s does not serve batch/v1 Job.spec.scheduling", serverVersion)
	}
	if _, found := job.Labels[kueue.MultiKueueOriginLabel]; found {
		return "the Job is a copy dispatched by a MultiKueue manager"
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj, "spec", "scheduling"); found {
		return "the Job sets spec.scheduling"
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj, "spec", "template", "spec", "schedulingGroup"); found {
		return "the Pod template sets schedulingGroup"
	}
	parallelism := ptr.Deref(job.Spec.Parallelism, 1)
	if parallelism <= 1 {
		return "parallelism is not greater than 1"
	}
	if job.Spec.Completions == nil {
		return "completions is unset"
	}
	if *job.Spec.Completions != parallelism {
		return fmt.Sprintf("completions (%d) differs from parallelism (%d)", *job.Spec.Completions, parallelism)
	}
	if features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.Enabled(job) {
		return "the Job opts into workload slices"
	}
	return ""
}

func (w *JobWebhook) serverVersion() *versionutil.Version {
	if w.kubeServerVersion == nil {
		return nil
	}
	v := w.kubeServerVersion.GetServerVersion()
	// A fetcher that has not completed a fetch holds a zero Version, whose
	// Major and Minor accessors index an empty slice.
	if len(v.Components()) < 2 || (v.Major() == 0 && v.Minor() == 0) {
		return nil
	}
	return &v
}

// CheckDefaultedFields reports, as an event on the Job, whether the API
// server kept the spec.scheduling that Kueue set at admission.
func (j *Job) CheckDefaultedFields(ctx context.Context, c client.Client, recorder events.EventRecorder) error {
	if j.Annotations[GangDefaultedAnnotation] == "" {
		return nil
	}
	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKeyFromObject(j.Object()), stored); err != nil {
		return client.IgnoreNotFound(err)
	}
	_, found, err := unstructured.NestedFieldNoCopy(stored.Object, "spec", "scheduling")
	if err != nil {
		return err
	}
	if !found {
		recorder.Eventf(j.Object(), nil, corev1.EventTypeWarning, ReasonGangSchedulingDefaultDropped, "GangSchedulingDefaultDropped",
			"Kueue set spec.scheduling at admission but the API server did not store it; the Job runs without gang scheduling. Check the WorkloadWithJob feature gate on the API server.")
		return nil
	}
	recorder.Eventf(j.Object(), nil, corev1.EventTypeNormal, ReasonGangSchedulingDefaulted, "GangSchedulingDefaulted",
		"Kueue set the gang scheduling policy with disruptionMode all")
	return nil
}
