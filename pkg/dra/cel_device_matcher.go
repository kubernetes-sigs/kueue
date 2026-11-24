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

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// maxCELSearchSteps caps the device visits of the search after the first pass.
const maxCELSearchSteps = 1_000_000

// celDeviceMatcher finds distinct devices for the CEL requests of one claim. Like
// the kube-scheduler allocator, its search tries requests and devices in order
// and stops at the first CEL evaluation error.
type celDeviceMatcher struct {
	cl         client.Client
	devices    []dracel.Device
	requests   []celDeviceRequest
	basePath   *field.Path
	classCache map[string]*resourcev1.DeviceClass
	// alternatives[r] matches requests[r]; nil until first use.
	alternatives []*celAlternativeMatcher
	// used[d] reports whether a request holds devices[d].
	used []bool
	// steps counts device visits against maxCELSearchSteps.
	steps int
}

func newCELDeviceMatcher(cl client.Client, devices []dracel.Device, requests []celDeviceRequest, basePath *field.Path) *celDeviceMatcher {
	return &celDeviceMatcher{
		cl:           cl,
		devices:      devices,
		requests:     requests,
		basePath:     basePath,
		classCache:   make(map[string]*resourcev1.DeviceClass),
		alternatives: make([]*celAlternativeMatcher, len(requests)),
		used:         make([]bool, len(devices)),
	}
}

// celAlternativeMatcher evaluates the selectors of one request on each device.
type celAlternativeMatcher struct {
	deviceClassName string
	selectorsPath   *field.Path
	classSelectors  []dracel.CompilationResult
	selectors       []dracel.CompilationResult
	// results[d] caches whether devices[d] matches; errors are not cached.
	results []celMatchResult
}

type celMatchResult uint8

const (
	celNotEvaluated celMatchResult = iota
	celMatching
	celNotMatching
)

// check returns the errors that the first pass or the search reaches, and the
// first shortage that takeFirstMatches finds, which the workload controller
// retries. It returns nil when the search finds distinct devices for every request,
// or when it reaches maxCELSearchSteps: kube-scheduler still decides per node, and a
// retry would stop at the same limit.
func (m *celDeviceMatcher) check(ctx context.Context) field.ErrorList {
	shortage, errs := m.takeFirstMatches(ctx)
	if shortage == nil {
		return errs
	}
	// The search would stop at the same error.
	if errs != nil {
		return append(errs, shortage)
	}
	clear(m.used)
	found, errs := m.search(ctx, 0)
	if found {
		return nil
	}
	if m.outOfSteps() {
		ctrl.LoggerFrom(ctx).V(3).Info("CEL device search reached its step limit, letting the claim through",
			"maxSteps", maxCELSearchSteps, "shortage", shortage.Field)
		return nil
	}
	return append(errs, shortage)
}

// takeFirstMatches gives each request in turn its first matching devices. It
// returns the shortage of the first request that falls short, and the errors of
// the requests before it.
func (m *celDeviceMatcher) takeFirstMatches(ctx context.Context) (*field.Error, field.ErrorList) {
	var allErrs field.ErrorList
	for r := range m.requests {
		shortage, errs := m.takeFirstMatch(ctx, r)
		allErrs = append(allErrs, errs...)
		if shortage != nil {
			return shortage, allErrs
		}
	}
	return nil, allErrs
}

// takeFirstMatch gives request r its first unused matching devices, or returns
// its shortage.
func (m *celDeviceMatcher) takeFirstMatch(ctx context.Context, r int) (*field.Error, field.ErrorList) {
	cr := m.requests[r]
	am, errs := m.alternative(ctx, r)
	if errs != nil {
		return nil, errs
	}
	// Requests with allocation mode All have a count of zero but still need one match.
	required := max(cr.count, 1)
	picked, errs := m.firstUnusedMatches(ctx, am, required)
	// Devices matched before an error stay taken, so that a later request that
	// needs them falls short and the workload controller retries the check.
	if errs != nil || int64(len(picked)) >= required {
		for _, d := range picked {
			m.used[d] = true
		}
		return nil, errs
	}
	// Cluster-state shortage: retryable until matching ResourceSlices are published.
	if cr.allDevices {
		return field.InternalError(
			am.selectorsPath,
			fmt.Errorf("insufficient matching devices for CEL selector in DeviceClass %s: allocationMode All requires at least one matching device", cr.deviceClassName),
		), nil
	}
	return field.InternalError(
		am.selectorsPath,
		fmt.Errorf("insufficient matching devices for CEL selector in DeviceClass %s: %d device(s) match in the cluster but %d requested",
			cr.deviceClassName, len(picked), cr.count),
	), nil
}

// firstUnusedMatches returns up to count unused devices that match am, in order.
// It stops at the first CEL evaluation error.
func (m *celDeviceMatcher) firstUnusedMatches(ctx context.Context, am *celAlternativeMatcher, count int64) ([]int, field.ErrorList) {
	var picked []int
	for d := range m.devices {
		if m.used[d] {
			continue
		}
		match, errs := am.matches(ctx, d, m.devices[d])
		if errs != nil {
			return picked, errs
		}
		if match {
			picked = append(picked, d)
			if int64(len(picked)) >= count {
				break
			}
		}
	}
	return picked, nil
}

// search reports whether unused devices can satisfy requests[r:].
func (m *celDeviceMatcher) search(ctx context.Context, r int) (bool, field.ErrorList) {
	if r == len(m.requests) {
		return true, nil
	}
	am, errs := m.alternative(ctx, r)
	if errs != nil {
		return false, errs
	}
	if m.requests[r].allDevices {
		return m.holdAll(ctx, r, am)
	}
	return m.pick(ctx, r, am, m.requests[r].count, 0)
}

// pick chooses the remaining devices of am from devices[from:], in index order so
// that each set is tried once, and then searches requests[r+1:].
func (m *celDeviceMatcher) pick(ctx context.Context, r int, am *celAlternativeMatcher, remaining int64, from int) (bool, field.ErrorList) {
	if remaining == 0 {
		return m.search(ctx, r+1)
	}
	if fits, errs := m.fits(ctx, am, remaining, from); !fits || errs != nil {
		return false, errs
	}
	for d := from; d < len(m.devices); d++ {
		m.steps++
		if m.outOfSteps() {
			return false, nil
		}
		if m.used[d] {
			continue
		}
		match, errs := am.matches(ctx, d, m.devices[d])
		if errs != nil {
			return false, errs
		}
		if !match {
			continue
		}
		m.used[d] = true
		found, errs := m.pick(ctx, r, am, remaining-1, d+1)
		m.used[d] = false
		if found || errs != nil || m.outOfSteps() {
			return found, errs
		}
	}
	return false, nil
}

// fits reports whether devices[from:] hold enough unused devices that may match
// am. If not, it still evaluates the unused ones in order, so that dropping this
// choice hides no CEL error that the allocator would reach.
func (m *celDeviceMatcher) fits(ctx context.Context, am *celAlternativeMatcher, remaining int64, from int) (bool, field.ErrorList) {
	var possible int64
	for d := from; d < len(m.devices) && possible < remaining; d++ {
		m.steps++
		if m.outOfSteps() {
			return false, nil
		}
		if !m.used[d] && am.results[d] != celNotMatching {
			possible++
		}
	}
	if possible >= remaining {
		return true, nil
	}
	for d := from; d < len(m.devices); d++ {
		m.steps++
		if m.outOfSteps() {
			return false, nil
		}
		if m.used[d] {
			continue
		}
		if _, errs := am.matches(ctx, d, m.devices[d]); errs != nil {
			return false, errs
		}
	}
	return false, nil
}

// holdAll holds every device that matches request r, an allocationMode All request,
// and then searches requests[r+1:]. Like the allocator, it needs at least one match
// and fails when another request holds one of them.
func (m *celDeviceMatcher) holdAll(ctx context.Context, r int, am *celAlternativeMatcher) (bool, field.ErrorList) {
	var held []int
	conflict := false
	for d := range m.devices {
		m.steps++
		if m.outOfSteps() {
			return false, nil
		}
		match, errs := am.matches(ctx, d, m.devices[d])
		if errs != nil {
			return false, errs
		}
		if match {
			conflict = conflict || m.used[d]
			held = append(held, d)
		}
	}
	if conflict || len(held) == 0 {
		return false, nil
	}
	for _, d := range held {
		m.used[d] = true
	}
	found, errs := m.search(ctx, r+1)
	for _, d := range held {
		m.used[d] = false
	}
	return found, errs
}

func (m *celDeviceMatcher) outOfSteps() bool {
	return m.steps > maxCELSearchSteps
}

// alternative returns the matcher of requests[r], built on first use.
func (m *celDeviceMatcher) alternative(ctx context.Context, r int) (*celAlternativeMatcher, field.ErrorList) {
	if am := m.alternatives[r]; am != nil {
		return am, nil
	}
	cr := m.requests[r]
	selectorsPath := m.requestPath(r).Child("exactly", "selectors")
	classSelectors, err := resolveAndCompileDeviceClass(ctx, m.cl, cr.deviceClassName, m.classCache, m.basePath, cr.index)
	if err != nil {
		return nil, field.ErrorList{err}
	}
	selectors, errs := compileCELSelectors(cr.selectors, selectorsPath, "CEL compilation failed")
	if len(errs) > 0 {
		return nil, errs
	}
	am := &celAlternativeMatcher{
		deviceClassName: cr.deviceClassName,
		selectorsPath:   selectorsPath,
		classSelectors:  classSelectors,
		selectors:       selectors,
		results:         make([]celMatchResult, len(m.devices)),
	}
	m.alternatives[r] = am
	return am, nil
}

func (m *celDeviceMatcher) requestPath(r int) *field.Path {
	return m.basePath.Child("devices", "requests").Index(m.requests[r].index)
}

// matches reports whether device d matches the class and request selectors.
func (am *celAlternativeMatcher) matches(ctx context.Context, d int, dev dracel.Device) (bool, field.ErrorList) {
	if am.results[d] != celNotEvaluated {
		return am.results[d] == celMatching, nil
	}
	match, err := evaluateSelectorsOnDevice(ctx, am.classSelectors, dev)
	if err == nil && match {
		match, err = evaluateSelectorsOnDevice(ctx, am.selectors, dev)
	}
	if err != nil {
		ctrl.LoggerFrom(ctx).V(4).Info("CEL evaluation error encountered while matching devices",
			"deviceClass", klog.KRef("", am.deviceClassName), "error", err)
		return false, field.ErrorList{field.Invalid(
			am.selectorsPath,
			nil,
			fmt.Sprintf("CEL evaluation failed for DeviceClass %s: %v", am.deviceClassName, err),
		)}
	}
	am.results[d] = celNotMatching
	if match {
		am.results[d] = celMatching
	}
	return match, nil
}
