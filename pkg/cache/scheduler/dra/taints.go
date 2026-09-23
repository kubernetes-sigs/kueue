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

package dra

import (
	"context"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyDeviceTaintRules returns the slices with DeviceTaintRule taints added, matching
// what resourceslice/tracker gives kube-scheduler. The tracker takes client-go typed
// informers, so reusing it would mean a second cache of every ResourceSlice.
func applyDeviceTaintRules(ctx context.Context, cl client.Client, served bool, deviceSlices []*resourceapi.ResourceSlice) ([]*resourceapi.ResourceSlice, error) {
	if !served || !utilfeature.DefaultFeatureGate.Enabled(kubefeatures.DRADeviceTaintRules) {
		return deviceSlices, nil
	}
	rules, err := deviceTaintRules(ctx, cl)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return deviceSlices, nil
	}
	tainted := make([]*resourceapi.ResourceSlice, len(deviceSlices))
	for i, slice := range deviceSlices {
		tainted[i] = applyRulesToSlice(slice, rules)
	}
	return tainted, nil
}

func deviceTaintRules(ctx context.Context, cl client.Client) ([]*resourceapi.DeviceTaintRule, error) {
	var ruleList resourceapi.DeviceTaintRuleList
	if err := cl.List(ctx, &ruleList); err != nil {
		return nil, fmt.Errorf("listing DeviceTaintRules: %w", err)
	}
	rules := make([]*resourceapi.DeviceTaintRule, len(ruleList.Items))
	for i := range ruleList.Items {
		rules[i] = &ruleList.Items[i]
	}
	return rules, nil
}

// applyRulesToSlice copies the slice on its first matching device only, so unmatched
// slices stay the client's objects.
func applyRulesToSlice(slice *resourceapi.ResourceSlice, rules []*resourceapi.DeviceTaintRule) *resourceapi.ResourceSlice {
	var patched *resourceapi.ResourceSlice
	for _, rule := range rules {
		deviceName, matches := determineDeviceName(rule.Spec.DeviceSelector, slice)
		if !matches {
			continue
		}
		for i := range slice.Spec.Devices {
			if deviceName != nil && *deviceName != slice.Spec.Devices[i].Name {
				continue
			}
			if patched == nil {
				patched = slice.DeepCopy()
			}
			patched.Spec.Devices[i].Taints = append(patched.Spec.Devices[i].Taints, rule.Spec.Taint)
		}
	}
	if patched == nil {
		return slice
	}
	return patched
}

// determineDeviceName reports whether the selector matches the slice, and the device it
// narrows the match to, if any.
func determineDeviceName(selector *resourceapi.DeviceTaintSelector, slice *resourceapi.ResourceSlice) (*string, bool) {
	// No selector taints every device. The API comment says it matches nothing, but the
	// tracker, which is what kube-scheduler sees, taints everything.
	if selector == nil {
		return nil, true
	}
	if selector.Driver != nil && *selector.Driver != slice.Spec.Driver {
		return nil, false
	}
	if selector.Pool != nil && *selector.Pool != slice.Spec.Pool.Name {
		return nil, false
	}
	return selector.Device, true
}
