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

package provisioning

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

func isProvisioned(pr *autoscaling.ProvisioningRequest) bool {
	return apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.Provisioned)
}

func isAccepted(pr *autoscaling.ProvisioningRequest) bool {
	return apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.Accepted)
}

func isFailed(pr *autoscaling.ProvisioningRequest) bool {
	return apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.Failed)
}

func isBookingExpired(pr *autoscaling.ProvisioningRequest) bool {
	return apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.BookingExpired)
}

func isCapacityRevoked(pr *autoscaling.ProvisioningRequest) bool {
	return apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.CapacityRevoked)
}

func ProvisioningRequestName(workloadName string, checkName kueue.AdmissionCheckReference, attempt int32) string {
	fullName := fmt.Sprintf("%s-%s-%d", workloadName, checkName, int(attempt))
	return limitObjectName(fullName)
}

func getProvisioningRequestNamePrefix(workloadName string, checkName kueue.AdmissionCheckReference) string {
	fullName := fmt.Sprintf("%s-%s-", workloadName, checkName)
	return limitObjectName(fullName)
}

func getProvisioningRequestPodTemplateName(prName string, podsetName kueue.PodSetReference) string {
	fullName := fmt.Sprintf("%s-%s-%s", podTemplatesPrefix, prName, podsetName)
	return limitObjectName(fullName)
}

func matchesWorkloadAndCheck(pr *autoscaling.ProvisioningRequest, workloadName string, checkName kueue.AdmissionCheckReference) bool {
	attemptRegex := getAttemptRegex(workloadName, checkName)
	matches := attemptRegex.FindStringSubmatch(pr.Name)
	return len(matches) > 0
}

func getAttempt(log logr.Logger, pr *autoscaling.ProvisioningRequest, workloadName string, checkName kueue.AdmissionCheckReference) int32 {
	attemptRegex := getAttemptRegex(workloadName, checkName)
	matches := attemptRegex.FindStringSubmatch(pr.Name)
	if len(matches) > 0 {
		number, err := strconv.Atoi(matches[1])
		if err != nil {
			log.Error(err, "Parsing the attempt number from provisioning request", "requestName", pr.Name)
			return 1
		}
		return int32(number)
	}
	log.Error(errors.New("no attempt suffix in provisioning request"), "No attempt suffix in provisioning request", "requestName", pr.Name)
	return 1
}

func getAttemptRegex(workloadName string, checkName kueue.AdmissionCheckReference) *regexp.Regexp {
	prefix := getProvisioningRequestNamePrefix(workloadName, checkName)
	escapedPrefix := regexp.QuoteMeta(prefix)
	return regexp.MustCompile("^" + escapedPrefix + "([0-9]+)$")
}

func parametersKueueToProvisioning(in map[string]kueue.Parameter) map[string]autoscaling.Parameter {
	if in == nil {
		return nil
	}

	out := make(map[string]autoscaling.Parameter, len(in))
	for k, v := range in {
		out[k] = autoscaling.Parameter(v)
	}
	return out
}

// provReqSyncedWithConfig checks if the provisioning request has the same provisioningClassName as the provisioning request config
// and contains all the parameters from the config
func provReqSyncedWithConfig(req *autoscaling.ProvisioningRequest, prc *kueue.ProvisioningRequestConfig) bool {
	if req.Spec.ProvisioningClassName != prc.Spec.ProvisioningClassName {
		return false
	}
	for k, vCfg := range prc.Spec.Parameters {
		if vReq, found := req.Spec.Parameters[k]; !found || string(vReq) != string(vCfg) {
			return false
		}
	}
	return true
}

// passProvReqParams extracts from Workload's annotations ones that should be passed to ProvisioningRequest
func passProvReqParams(wl *kueue.Workload, req *autoscaling.ProvisioningRequest) {
	if req.Spec.Parameters == nil {
		req.Spec.Parameters = make(map[string]autoscaling.Parameter, 0)
	}
	for annotation, val := range admissioncheck.FilterProvReqAnnotations(wl.Annotations) {
		paramName := strings.TrimPrefix(annotation, constants.ProvReqAnnotationPrefix)
		req.Spec.Parameters[paramName] = autoscaling.Parameter(val)
	}
}
