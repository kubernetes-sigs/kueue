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

package jobframework

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/set"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	errDuplicateFrameworkName = errors.New("duplicate framework name")
	errMissingMandatoryField  = errors.New("mandatory field missing")
	errFrameworkNameFormat    = errors.New("misformatted external framework name")

	errIntegrationNotFound             = errors.New("integration not found")
	errDependencyIntegrationNotEnabled = errors.New("integration not enabled")
)

type JobReconcilerInterface interface {
	reconcile.Reconciler
	SetupWithManager(mgr ctrl.Manager) error
}

type ReconcilerFactory func(client client.Client, record record.EventRecorder, opts ...Option) JobReconcilerInterface

// IntegrationCallbacks groups a set of callbacks used to integrate a new framework.
type IntegrationCallbacks struct {
	// NewJob creates a new instance of job
	NewJob func() GenericJob
	// GVK holds the schema information for the job
	// (this callback is optional)
	GVK schema.GroupVersionKind
	// NewReconciler creates a new reconciler
	NewReconciler ReconcilerFactory
	// SetupWebhook sets up the framework's webhook with the controllers manager
	SetupWebhook func(mgr ctrl.Manager, opts ...Option) error
	// JobType holds an object of the type managed by the integration's webhook
	JobType runtime.Object
	// SetupIndexes registers any additional indexes with the controllers manager
	// (this callback is optional)
	SetupIndexes func(ctx context.Context, indexer client.FieldIndexer) error
	// AddToScheme adds any additional types to the controllers manager's scheme
	// (this callback is optional)
	AddToScheme func(s *runtime.Scheme) error
	// Returns true if the provided owner reference identifies an object
	// managed by this integration
	// (this callback is optional)
	IsManagingObjectsOwner func(ref *metav1.OwnerReference) bool
	// CanSupportIntegration returns true if the integration meets any additional condition
	// like the Kubernetes version.
	CanSupportIntegration func(opts ...Option) (bool, error)
	// The job's MultiKueue adapter (optional)
	MultiKueueAdapter MultiKueueAdapter
	// The list of integration that need to be enabled along with the current one.
	DependencyList []string
}

type integrationManager struct {
	names                []string
	integrations         map[string]IntegrationCallbacks
	enabledIntegrations  set.Set[string]
	externalIntegrations map[string]runtime.Object
	mu                   sync.RWMutex
}

var manager integrationManager

func (m *integrationManager) register(name string, cb IntegrationCallbacks) error {
	if m.integrations == nil {
		m.integrations = make(map[string]IntegrationCallbacks)
	}
	if _, exists := m.integrations[name]; exists {
		return fmt.Errorf("%w %q", errDuplicateFrameworkName, name)
	}

	if cb.NewReconciler == nil {
		return fmt.Errorf("%w \"NewReconciler\" for %q", errMissingMandatoryField, name)
	}

	if cb.SetupWebhook == nil {
		return fmt.Errorf("%w \"SetupWebhook\" for %q", errMissingMandatoryField, name)
	}

	if cb.JobType == nil {
		return fmt.Errorf("%w \"WebhookType\" for %q", errMissingMandatoryField, name)
	}

	m.integrations[name] = cb
	m.names = append(m.names, name)

	return nil
}

func (m *integrationManager) registerExternal(kindArg string) error {
	if m.externalIntegrations == nil {
		m.externalIntegrations = make(map[string]runtime.Object)
	}

	gvk, _ := schema.ParseKindArg(kindArg)
	if gvk == nil {
		return fmt.Errorf("%w %q", errFrameworkNameFormat, kindArg)
	}
	apiVersion, kind := gvk.ToAPIVersionAndKind()
	jobType := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiVersion,
			Kind:       kind,
		},
	}

	m.externalIntegrations[kindArg] = jobType

	return nil
}

func (m *integrationManager) forEach(f func(name string, cb IntegrationCallbacks) error) error {
	for _, name := range m.names {
		if err := f(name, m.integrations[name]); err != nil {
			return err
		}
	}
	return nil
}

func (m *integrationManager) get(name string) (IntegrationCallbacks, bool) {
	cb, f := m.integrations[name]
	return cb, f
}

func (m *integrationManager) getExternal(kindArg string) (runtime.Object, bool) {
	jt, f := m.externalIntegrations[kindArg]
	return jt, f
}

func (m *integrationManager) getEnabledIntegrations() set.Set[string] {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabledIntegrations.Clone()
}

func (m *integrationManager) enableIntegration(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabledIntegrations == nil {
		m.enabledIntegrations = set.New(name)
	} else {
		m.enabledIntegrations.Insert(name)
	}
}

func (m *integrationManager) getList() []string {
	ret := make([]string, len(m.names))
	copy(ret, m.names)
	sort.Strings(ret)
	return ret
}

func (m *integrationManager) getJobTypeForOwner(ownerRef *metav1.OwnerReference) runtime.Object {
	for jobKey := range m.getEnabledIntegrations() {
		cbs, found := m.integrations[jobKey]
		if found && cbs.IsManagingObjectsOwner != nil && cbs.IsManagingObjectsOwner(ownerRef) {
			return cbs.JobType
		}
	}
	for _, jt := range m.externalIntegrations {
		apiVersion, kind := jt.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		if ownerRef.Kind == kind && ownerRef.APIVersion == apiVersion {
			return jt
		}
	}

	return nil
}

func (m *integrationManager) checkEnabledListDependencies(enabledSet sets.Set[string]) error {
	enabled := enabledSet.UnsortedList()
	slices.Sort(enabled)
	for _, integration := range enabled {
		cbs, found := m.integrations[integration]
		if !found {
			return fmt.Errorf("%q %w", integration, errIntegrationNotFound)
		}
		for _, dep := range cbs.DependencyList {
			if !enabledSet.Has(dep) {
				return fmt.Errorf("%q %w %q", integration, errDependencyIntegrationNotEnabled, dep)
			}
		}
	}
	return nil
}

// RegisterIntegration registers a new framework, returns an error when
// attempting to register multiple frameworks with the same name or if a
// mandatory callback is missing.
func RegisterIntegration(name string, cb IntegrationCallbacks) error {
	return manager.register(name, cb)
}

// RegisterExternalJobType registers a new externally-managed Kind, returns an error
// if kindArg cannot be parsed as a Kind.version.group.
func RegisterExternalJobType(kindArg string) error {
	return manager.registerExternal(kindArg)
}

// ForEachIntegration loops through the registered list of frameworks calling f,
// if at any point f returns an error the loop is stopped and that error is returned.
func ForEachIntegration(f func(name string, cb IntegrationCallbacks) error) error {
	return manager.forEach(f)
}

// EnableIntegration marks the integration identified by name as enabled.
func EnableIntegration(name string) {
	manager.enableIntegration(name)
}

// EnableIntegrationsForTest - should be used only in tests
// Mark the frameworks identified by names and return a revert function.
func EnableIntegrationsForTest(tb testing.TB, names ...string) func() {
	tb.Helper()
	old := manager.getEnabledIntegrations()
	for _, name := range names {
		manager.enableIntegration(name)
	}
	return func() {
		manager.mu.Lock()
		manager.enabledIntegrations = old
		manager.mu.Unlock()
	}
}

// GetIntegration looks-up the framework identified by name in the currently registered
// list of frameworks returning its callbacks and true if found.
func GetIntegration(name string) (IntegrationCallbacks, bool) {
	return manager.get(name)
}

// GetIntegrationByGVK looks-up the framework identified by GroupVersionKind in the currently
// registered list of frameworks returning its callbacks and true if found.
func GetIntegrationByGVK(gvk schema.GroupVersionKind) (IntegrationCallbacks, bool) {
	for _, name := range manager.getList() {
		integration, ok := GetIntegration(name)
		if ok && matchingGVK(integration, gvk) {
			return integration, true
		}
	}
	return IntegrationCallbacks{}, false
}

func matchingGVK(integration IntegrationCallbacks, gvk schema.GroupVersionKind) bool {
	if integration.NewJob != nil {
		return gvk == integration.NewJob().GVK()
	} else {
		return gvk == integration.GVK
	}
}

// GetIntegrationsList returns the list of currently registered frameworks.
func GetIntegrationsList() []string {
	return manager.getList()
}

// IsOwnerManagedByKueue returns true if the provided owner can be managed by
// kueue.
func IsOwnerManagedByKueue(owner *metav1.OwnerReference) bool {
	return manager.getJobTypeForOwner(owner) != nil
}

// GetEmptyOwnerObject returns an empty object of the owner's type,
// returns nil if the owner is not manageable by kueue.
func GetEmptyOwnerObject(owner *metav1.OwnerReference) client.Object {
	if jt := manager.getJobTypeForOwner(owner); jt != nil {
		return jt.DeepCopyObject().(client.Object)
	}
	return nil
}

// GetMultiKueueAdapters returns the map containing the MultiKueue adapters for the
// registered and enabled integrations.
// An error is returned if more then one adapter is registers for one object type.
func GetMultiKueueAdapters(enabledIntegrations sets.Set[string]) (map[string]MultiKueueAdapter, error) {
	ret := map[string]MultiKueueAdapter{}
	if err := manager.forEach(func(intName string, cb IntegrationCallbacks) error {
		if cb.MultiKueueAdapter != nil && enabledIntegrations.Has(intName) {
			gvk := cb.MultiKueueAdapter.GVK().String()
			if _, found := ret[gvk]; found {
				return fmt.Errorf("multiple adapters for GVK: %q", gvk)
			}
			ret[gvk] = cb.MultiKueueAdapter
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return ret, nil
}
