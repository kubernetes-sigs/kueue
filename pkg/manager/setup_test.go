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

package manager

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

func TestConfigApply_WithDefaultConfig_Succeeds(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error for default config: %v", err)
	}
	if cfg.Apiconf.Namespace == nil || *cfg.Apiconf.Namespace == "" {
		t.Fatalf("expected Namespace to be defaulted, got: %#v", cfg.Apiconf.Namespace)
	}
}

func TestBlockForPodsReady_Default_IsFalse(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error for default config: %v", err)
	}
	if cfg.BlockForPodsReady() {
		t.Fatalf("expected BlockForPodsReady to be false by default")
	}
}

func TestPodsReadyRequeuingTimestamp_Default_IsEviction(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error for default config: %v", err)
	}
	if got := cfg.PodsReadyRequeuingTimestamp(); got != configapi.EvictionTimestamp {
		t.Fatalf("expected default requeuing timestamp %q, got %q", configapi.EvictionTimestamp, got)
	}
}

func TestNewConfig_InitializesSuccessfully(t *testing.T) {
	cfg := NewConfig()

	if cfg == nil {
		t.Fatal("expected config to be created")
	}

	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	if cfg.Options.Scheme == nil {
		t.Fatal("expected scheme to be initialized after Apply")
	}
}

func TestConfigApply_WithInvalidFile_ReturnsError(t *testing.T) {
	cfg := NewConfig()
	err := cfg.Apply("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for non-existent config file")
	}
}

func TestBlockForPodsReady_WhenEnabled_ReturnsTrue(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	enabled := true
	blockAdmission := true
	cfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{
		Enable:         enabled,
		BlockAdmission: &blockAdmission,
	}

	if !cfg.BlockForPodsReady() {
		t.Fatal("expected BlockForPodsReady to be true when enabled")
	}
}

func TestBlockForPodsReady_WhenEnabledButNotBlocking_ReturnsFalse(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	enabled := true
	blockAdmission := false
	cfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{
		Enable:         enabled,
		BlockAdmission: &blockAdmission,
	}

	if cfg.BlockForPodsReady() {
		t.Fatal("expected BlockForPodsReady to be false when not blocking")
	}
}

func TestPodsReadyRequeuingTimestamp_WithCreationStrategy_ReturnsCreation(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	creationTimestamp := configapi.CreationTimestamp
	cfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{
		RequeuingStrategy: &configapi.RequeuingStrategy{
			Timestamp: &creationTimestamp,
		},
	}

	if got := cfg.PodsReadyRequeuingTimestamp(); got != configapi.CreationTimestamp {
		t.Fatalf("expected requeuing timestamp %q, got %q", configapi.CreationTimestamp, got)
	}
}

func TestPodsReadyRequeuingTimestamp_WithEvictionStrategy_ReturnsEviction(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	evictionTimestamp := configapi.EvictionTimestamp
	cfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{
		RequeuingStrategy: &configapi.RequeuingStrategy{
			Timestamp: &evictionTimestamp,
		},
	}

	if got := cfg.PodsReadyRequeuingTimestamp(); got != configapi.EvictionTimestamp {
		t.Fatalf("expected requeuing timestamp %q, got %q", configapi.EvictionTimestamp, got)
	}
}

func TestPodsReadyRequeuingTimestamp_WithNilStrategy_ReturnsEviction(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	cfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{}

	if got := cfg.PodsReadyRequeuingTimestamp(); got != configapi.EvictionTimestamp {
		t.Fatalf("expected default requeuing timestamp %q, got %q", configapi.EvictionTimestamp, got)
	}
}

func TestConfigApply_SetsNamespace(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	if cfg.Apiconf.Namespace == nil {
		t.Fatal("expected Namespace to be set")
	}

	if *cfg.Apiconf.Namespace == "" {
		t.Fatal("expected Namespace to have a value")
	}
}

func TestConfigApply_SetsMetricsBindAddress(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	if cfg.Apiconf.Metrics.BindAddress == "" {
		t.Fatal("expected Metrics.BindAddress to be set")
	}
}

func TestConfigApply_SetsHealthProbeBindAddress(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	if cfg.Options.HealthProbeBindAddress == "" {
		t.Fatal("expected HealthProbeBindAddress to be set")
	}
}

type mockManager struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
}

func (m *mockManager) GetClient() client.Client {
	return m.client
}

func (m *mockManager) GetScheme() *runtime.Scheme {
	return m.scheme
}

func (m *mockManager) GetEventRecorderFor(name string) record.EventRecorder {
	return m.recorder
}

func (m *mockManager) Add(manager.Runnable) error                              { return nil }
func (m *mockManager) Elected() <-chan struct{}                                { return nil }
func (m *mockManager) AddMetricsServerExtraHandler(string, http.Handler) error { return nil }
func (m *mockManager) AddHealthzCheck(string, healthz.Checker) error           { return nil }
func (m *mockManager) AddReadyzCheck(string, healthz.Checker) error            { return nil }
func (m *mockManager) Start(context.Context) error                             { return nil }
func (m *mockManager) GetConfig() *rest.Config                                 { return nil }
func (m *mockManager) GetRESTMapper() meta.RESTMapper                          { return nil }
func (m *mockManager) GetAPIReader() client.Reader                             { return m.client }
func (m *mockManager) GetFieldIndexer() client.FieldIndexer                    { return nil }
func (m *mockManager) GetCache() cache.Cache                                   { return nil }
func (m *mockManager) GetControllerOptions() config.Controller                 { return config.Controller{} }
func (m *mockManager) GetLogger() logr.Logger                                  { return logr.Discard() }
func (m *mockManager) GetWebhookServer() webhook.Server                        { return nil }
func (m *mockManager) GetHTTPClient() *http.Client                             { return nil }

type mockEventRecorder struct{}

func (m *mockEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {}
func (m *mockEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...any) {
}
func (m *mockEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...any) {
}

func TestSetupScheduler_WithValidInputs(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mockMgr := &mockManager{
		client:   fakeClient,
		scheme:   scheme,
		recorder: &mockEventRecorder{},
	}

	cCache := schdcache.New(fakeClient)
	queues := qcache.NewManager(fakeClient, cCache)

	err := cfg.SetupScheduler(mockMgr, cCache, queues)
	if err != nil {
		t.Fatalf("SetupScheduler() returned error: %v", err)
	}

	t.Log("SetupScheduler completed successfully")
}

func TestSetupScheduler_WithNilCache(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mockMgr := &mockManager{
		client:   fakeClient,
		scheme:   scheme,
		recorder: &mockEventRecorder{},
	}

	queues := qcache.NewManager(fakeClient, nil)

	err := cfg.SetupScheduler(mockMgr, nil, queues)
	if err != nil {
		t.Fatalf("SetupScheduler() returned error: %v", err)
	}
}

func TestSetupScheduler_WithFairSharingEnabled(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	cfg.Apiconf.FairSharing = &configapi.FairSharing{
		Enable: true,
	}

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mockMgr := &mockManager{
		client:   fakeClient,
		scheme:   scheme,
		recorder: &mockEventRecorder{},
	}

	cCache := schdcache.New(fakeClient)
	queues := qcache.NewManager(fakeClient, cCache)

	err := cfg.SetupScheduler(mockMgr, cCache, queues)
	if err != nil {
		t.Fatalf("SetupScheduler() with fair sharing returned error: %v", err)
	}

	t.Log("SetupScheduler with fair sharing completed successfully")
}

func TestSetupProbeEndpoints_WithClosedChannel(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	healthzCalled := false
	readyzCalled := false

	mockMgr := &mockManagerWithHealthChecks{
		onAddHealthz: func() { healthzCalled = true },
		onAddReadyz:  func() { readyzCalled = true },
	}

	certsReady := make(chan struct{})
	close(certsReady)

	err := cfg.SetupProbeEndpoints(mockMgr, certsReady)
	if err != nil {
		t.Fatalf("SetupProbeEndpoints() returned error: %v", err)
	}

	if !healthzCalled {
		t.Error("expected AddHealthzCheck to be called")
	}
	if !readyzCalled {
		t.Error("expected AddReadyzCheck to be called")
	}

	t.Log("SetupProbeEndpoints completed successfully")
}

func TestSetupProbeEndpoints_ReadyzReturnsErrorWhenCertsNotReady(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	var capturedReadyzChecker healthz.Checker

	mockMgr := &mockManagerWithHealthChecks{
		onAddHealthz: func() {},
		onAddReadyz:  func() {},
		captureReadyzChecker: func(checker healthz.Checker) {
			capturedReadyzChecker = checker
		},
	}

	certsReady := make(chan struct{})

	err := cfg.SetupProbeEndpoints(mockMgr, certsReady)
	if err != nil {
		t.Fatalf("SetupProbeEndpoints() returned error: %v", err)
	}

	if capturedReadyzChecker == nil {
		t.Fatal("readyz checker was not captured")
	}

	err = capturedReadyzChecker(nil)
	if err == nil {
		t.Fatal("expected readyz checker to return error when certs not ready")
	}

	if err.Error() != "certificates are not ready" {
		t.Errorf("expected error 'certificates are not ready', got: %v", err)
	}

	t.Log("Readyz checker correctly returns error when certs not ready")
}

func TestSetupProbeEndpoints_ReadyzSucceedsWhenCertsReady(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Apply(""); err != nil {
		t.Fatalf("Apply() returned error: %v", err)
	}

	var capturedReadyzChecker healthz.Checker

	mockMgr := &mockManagerWithHealthChecks{
		onAddHealthz: func() {},
		onAddReadyz:  func() {},
		captureReadyzChecker: func(checker healthz.Checker) {
			capturedReadyzChecker = checker
		},
	}

	certsReady := make(chan struct{})
	close(certsReady)

	err := cfg.SetupProbeEndpoints(mockMgr, certsReady)
	if err != nil {
		t.Fatalf("SetupProbeEndpoints() returned error: %v", err)
	}

	if capturedReadyzChecker == nil {
		t.Fatal("readyz checker was not captured")
	}

	err = capturedReadyzChecker(nil)
	if err != nil {
		t.Errorf("expected readyz checker to succeed when certs ready, got error: %v", err)
	}

	t.Log("Readyz checker correctly succeeds when certs ready")
}

type mockManagerWithHealthChecks struct {
	mockManager
	onAddHealthz         func()
	onAddReadyz          func()
	captureReadyzChecker func(healthz.Checker)
}

func (m *mockManagerWithHealthChecks) AddHealthzCheck(name string, check healthz.Checker) error {
	if m.onAddHealthz != nil {
		m.onAddHealthz()
	}
	return nil
}

func (m *mockManagerWithHealthChecks) AddReadyzCheck(name string, check healthz.Checker) error {
	if m.onAddReadyz != nil {
		m.onAddReadyz()
	}
	if m.captureReadyzChecker != nil {
		m.captureReadyzChecker(check)
	}
	return nil
}

func (m *mockManagerWithHealthChecks) GetWebhookServer() webhook.Server {
	return &mockWebhookServer{}
}

type mockWebhookServer struct{}

func (m *mockWebhookServer) StartedChecker() healthz.Checker {
	return func(*http.Request) error { return nil }
}

func (m *mockWebhookServer) Register(string, http.Handler) {}
func (m *mockWebhookServer) Start(context.Context) error   { return nil }
func (m *mockWebhookServer) NeedLeaderElection() bool      { return false }
func (m *mockWebhookServer) WebhookMux() *http.ServeMux    { return nil }
