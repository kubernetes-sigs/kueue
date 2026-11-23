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

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"kueueviz/middleware"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestParseDashboardPagination(t *testing.T) {
	cases := map[string]struct {
		query     string
		want      *dashboardPagination
		wantError bool
	}{
		"legacy":         {query: "namespace=ns"},
		"default size":   {query: "page=2", want: &dashboardPagination{Page: 2, PageSize: 25}},
		"default page":   {query: "pageSize=50", want: &dashboardPagination{PageSize: 50}},
		"both":           {query: "page=1&pageSize=100", want: &dashboardPagination{Page: 1, PageSize: 100}},
		"empty page":     {query: "page=", wantError: true},
		"empty size":     {query: "pageSize=", wantError: true},
		"negative page":  {query: "page=-1", wantError: true},
		"negative size":  {query: "pageSize=-1", wantError: true},
		"zero size":      {query: "pageSize=0", wantError: true},
		"oversized":      {query: "pageSize=101", wantError: true},
		"fractional":     {query: "page=1.5", wantError: true},
		"not integer":    {query: "page=abc", wantError: true},
		"overflow":       {query: "page=99999999999999999999", wantError: true},
		"duplicate page": {query: "page=0&page=1", wantError: true},
		"duplicate size": {query: "pageSize=25&pageSize=50", wantError: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			query, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseDashboardPagination(query)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected pagination (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDashboardPagination(t *testing.T) {
	finished := makeDashboardWorkload("a", "ns-a", "a1", "job-a")
	finished.Status.Conditions = []metav1.Condition{{Type: kueueapi.WorkloadFinished, Status: metav1.ConditionTrue}}
	cases := map[string]struct {
		namespace    string
		pagination   *dashboardPagination
		wantNames    []string
		wantPage     int
		wantTotal    int
		wantFinished int
	}{
		"first":        {pagination: &dashboardPagination{PageSize: 2}, wantNames: []string{"ns-a/a", "ns-a/z"}, wantTotal: 3, wantFinished: 1},
		"last":         {pagination: &dashboardPagination{Page: 1, PageSize: 2}, wantNames: []string{"ns-b/a"}, wantPage: 1, wantTotal: 3, wantFinished: 1},
		"past last":    {pagination: &dashboardPagination{Page: 99, PageSize: 2}, wantNames: []string{"ns-b/a"}, wantPage: 1, wantTotal: 3, wantFinished: 1},
		"max int page": {pagination: &dashboardPagination{Page: int(^uint(0) >> 1), PageSize: 2}, wantNames: []string{"ns-b/a"}, wantPage: 1, wantTotal: 3, wantFinished: 1},
		"namespace":    {namespace: "ns-a", pagination: &dashboardPagination{PageSize: 1}, wantNames: []string{"ns-a/a"}, wantTotal: 2, wantFinished: 1},
		"empty":        {namespace: "missing", pagination: &dashboardPagination{Page: 10, PageSize: 2}, wantNames: []string{}},
		"legacy":       {wantNames: []string{"ns-b/a", "ns-a/z", "ns-a/a"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := &fakeDashboardClient{workloads: []kueueapi.Workload{
				makeDashboardWorkload("a", "ns-b", "b1", "job-b"),
				makeDashboardWorkload("z", "ns-a", "a2", "job-z"),
				finished,
			}}
			h := &Handlers{client: client}
			data, _, err := h.fetchWorkloadsDashboardData(t.Context(), tc.namespace, middleware.Identity{}, tc.pagination)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			pageNamespaces := map[string]int{}
			for _, wl := range dashboardWorkloadItems(t, data) {
				names = append(names, wl.Namespace+"/"+wl.Name)
				pageNamespaces[wl.Namespace] = 1
			}
			if diff := cmp.Diff(tc.wantNames, names); diff != "" {
				t.Errorf("unexpected names (-want,+got):\n%s", diff)
			}
			if len(pageNamespaces) > 0 {
				if diff := cmp.Diff(pageNamespaces, client.podListCallsByNamespace); diff != "" {
					t.Errorf("unexpected pod queries (-want,+got):\n%s", diff)
				}
			} else if len(client.podListCallsByNamespace) != 0 {
				t.Error("empty page queried pods")
			}
			result := data.(map[string]any)
			if tc.pagination == nil {
				if _, exists := result["pagination"]; exists {
					t.Error("legacy response includes pagination")
				}
				if len(result["workloads_by_uid"].(map[types.UID]string)) != 3 {
					t.Error("legacy UID map is incomplete")
				}
				return
			}
			want := &dashboardPagination{Page: tc.wantPage, PageSize: tc.pagination.PageSize, TotalCount: tc.wantTotal}
			if diff := cmp.Diff(want, result["pagination"]); diff != "" {
				t.Errorf("unexpected pagination (-want,+got):\n%s", diff)
			}
			if result["finishedCount"] != tc.wantFinished {
				t.Errorf("finished count = %v, want %d", result["finishedCount"], tc.wantFinished)
			}
		})
	}
}

func TestPaginatedDashboardLinksOffPagePreemptor(t *testing.T) {
	victim := makeDashboardWorkload("victim", "ns-a", "abc-1", "job-victim")
	victim.Status.Conditions = []metav1.Condition{{Type: "Evicted", Reason: "Preempted", Status: metav1.ConditionTrue, Message: "Preempted (UID: abc-2, JobUID: abc-3)"}}
	client := &fakeDashboardClient{workloads: []kueueapi.Workload{
		victim,
		makeDashboardWorkload("preemptor", "ns-b", "abc-2", "job-preemptor"),
		makeDashboardWorkload("unrelated", "ns-b", "abc-3", "job-unrelated"),
	}}
	data, _, err := (&Handlers{client: client}).fetchWorkloadsDashboardData(t.Context(), "", middleware.Identity{}, &dashboardPagination{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := map[types.UID]string{"abc-1": "ns-a/victim", "abc-2": "ns-b/preemptor"}
	if diff := cmp.Diff(want, data.(map[string]any)["workloads_by_uid"]); diff != "" {
		t.Errorf("unexpected links (-want,+got):\n%s", diff)
	}
	data, _, err = (&Handlers{client: client}).fetchWorkloadsDashboardData(t.Context(), "ns-a", middleware.Identity{}, &dashboardPagination{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	want = map[types.UID]string{"abc-1": "ns-a/victim"}
	if diff := cmp.Diff(want, data.(map[string]any)["workloads_by_uid"]); diff != "" {
		t.Errorf("namespace filter leaked a preemptor link (-want,+got):\n%s", diff)
	}

}

func TestDashboardRejectsInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&Handlers{}).InitializeWebSocketRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	t.Run("HTTP", func(t *testing.T) {
		response, err := http.Get(server.URL + "/ws/workloads/dashboard?pageSize=0")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", response.StatusCode)
		}
	})
	t.Run("WebSocket", func(t *testing.T) {
		conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/workloads/dashboard?pageSize=0", nil)
		if response != nil {
			defer response.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
		_, _, err = conn.ReadMessage()
		if !websocket.IsCloseError(err, middleware.WSCloseBadRequest) {
			t.Fatalf("expected bad request close, got %v", err)
		}
	})
}

func BenchmarkDashboardPagination(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		for _, paginated := range []bool{false, true} {
			b.Run(fmt.Sprintf("workloads=%d/paginated=%t", count, paginated), func(b *testing.B) {
				client := &fakeDashboardClient{}
				for i := 0; i < count; i++ {
					client.workloads = append(client.workloads, makeDashboardWorkload(fmt.Sprintf("wl-%05d", i), "ns", fmt.Sprintf("uid-%d", i), fmt.Sprintf("job-%d", i)))
				}
				h := &Handlers{client: client}
				var pagination *dashboardPagination
				if paginated {
					pagination = &dashboardPagination{PageSize: 25}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					data, _, err := h.fetchWorkloadsDashboardData(b.Context(), "", middleware.Identity{}, pagination)
					if err != nil {
						b.Fatal(err)
					}
					body, err := json.Marshal(data)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportMetric(float64(len(body)), "response-bytes")
				}
			})
		}
	}
}

func TestPaginatedDashboardOmitsUnauthorizedPods(t *testing.T) {
	client := &fakeDashboardClient{workloads: []kueueapi.Workload{makeDashboardWorkload("wl", "ns", "uid", "job")}}
	h := &Handlers{client: client, authorizer: authorizerFunc(func(attrs authorizationv1.ResourceAttributes) (bool, error) {
		return attrs.Resource != "pods", nil
	})}
	data, omitted, err := h.fetchWorkloadsDashboardData(t.Context(), "ns", middleware.Identity{}, &dashboardPagination{PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if !omitted {
		t.Error("expected pods to be marked omitted")
	}
	if len(client.podListCallsByNamespace) != 0 {
		t.Error("queried unauthorized pods")
	}
	if len(dashboardWorkloadItems(t, data)) != 1 {
		t.Error("pod authorization incorrectly hides workload")
	}
}

func TestDashboardPaginationLiveUpdates(t *testing.T) {
	first := makeDashboardWorkload("a", "ns", "uid-a", "job-a")
	last := makeDashboardWorkload("b", "ns", "uid-b", "job-b")
	client := newFakeClient(t, nil, &first, &last).(fakeReaderClient)
	writer := client.Reader.(ctrlclient.Client)
	informer := client.informer.(*mockInformer)
	router := gin.New()
	New(client, nil, nil).InitializeWebSocketRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/workloads/dashboard?page=1&pageSize=1", nil)
	if response != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readPage := func(wantName string, wantPage, wantTotal, wantFinished int) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
		var result struct {
			Workloads struct {
				Items         []struct{ Metadata struct{ Name string } }
				Pagination    dashboardPagination
				FinishedCount int
			}
		}
		if err := conn.ReadJSON(&result); err != nil {
			t.Fatal(err)
		}
		if len(result.Workloads.Items) != 1 || result.Workloads.Items[0].Metadata.Name != wantName {
			t.Fatalf("unexpected page items: %+v", result.Workloads.Items)
		}
		want := dashboardPagination{Page: wantPage, PageSize: 1, TotalCount: wantTotal}
		if diff := cmp.Diff(want, result.Workloads.Pagination); diff != "" {
			t.Errorf("unexpected pagination (-want,+got):\n%s", diff)
		}
		if result.Workloads.FinishedCount != wantFinished {
			t.Errorf("finished count = %d, want %d", result.Workloads.FinishedCount, wantFinished)
		}
	}
	readPage("b", 1, 2, 0)
	waitUntil(t, testTimeout, testPollInterval, func() bool { return informer.AddEventHandlerCallCount() == 5 }, "informer registrations were not installed")
	if err := writer.Delete(t.Context(), &last); err != nil {
		t.Fatal(err)
	}
	informer.triggerAdd(&last)
	readPage("a", 0, 1, 0)
	added := makeDashboardWorkload("c", "ns", "uid-c", "job-c")
	added.Status.Conditions = []metav1.Condition{{Type: kueueapi.WorkloadFinished, Status: metav1.ConditionTrue}}
	if err := writer.Create(t.Context(), &added); err != nil {
		t.Fatal(err)
	}
	informer.triggerAdd(&added)
	// The request stays on page 1 even after its earlier response was clamped.
	readPage("c", 1, 2, 1)
}
