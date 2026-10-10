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

package create

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/dryrun"
)

func TestCreateLocalQueue(t *testing.T) {
	testCases := map[string]struct {
		options  *LocalQueueOptions
		expected *kueue.LocalQueue
	}{
		"success_create": {
			options: &LocalQueueOptions{
				Name:         "lq1",
				Namespace:    "ns1",
				ClusterQueue: "cq1",
			},
			expected: &kueue.LocalQueue{
				APIVersion: "kueue.x-k8s.io/v1beta2", Kind: "LocalQueue",
				Name: "lq1", Namespace: "ns1",
				Spec: kueue.LocalQueueSpec{ClusterQueue: "cq1"},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			lq := tc.options.createLocalQueue()
			if diff := cmp.Diff(tc.expected, lq); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateLocalQueue(t *testing.T) {
	testCases := map[string]struct {
		options *LocalQueueOptions
		wantErr string
	}{
		"missing name": {
			options: &LocalQueueOptions{
				ClusterQueue: "cq1",
				Namespace:    "ns1",
			},
			wantErr: "name must be specified",
		},
		"missing clusterqueue": {
			options: &LocalQueueOptions{
				Name:      "lq1",
				Namespace: "ns1",
			},
			wantErr: "clusterqueue must be specified",
		},
		"missing namespace": {
			options: &LocalQueueOptions{
				Name:         "lq1",
				ClusterQueue: "cq1",
			},
			wantErr: "namespace must be specified",
		},
		"existing cluster queue with dry-run none": {
			options: &LocalQueueOptions{
				Name:                      "lq1",
				Namespace:                 "ns1",
				ClusterQueue:              "cq1",
				UserSpecifiedClusterQueue: "cq1",
				DryRunStrategy:            dryrun.None,
				Client: fake.NewSimpleClientset(&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{Name: "cq1"},
				}).KueueV1beta2(),
			},
		},
		"unknown cluster queue with dry-run none fails": {
			options: &LocalQueueOptions{
				Name:                      "lq1",
				Namespace:                 "ns1",
				ClusterQueue:              "unknown-cq",
				UserSpecifiedClusterQueue: "unknown-cq",
				DryRunStrategy:            dryrun.None,
				Client:                    fake.NewSimpleClientset().KueueV1beta2(),
			},
			wantErr: `clusterqueues.kueue.x-k8s.io "unknown-cq" not found`,
		},
		"unknown cluster queue with dry-run none and ignore-unknown-cq succeeds": {
			options: &LocalQueueOptions{
				Name:                      "lq1",
				Namespace:                 "ns1",
				ClusterQueue:              "unknown-cq",
				UserSpecifiedClusterQueue: "unknown-cq",
				IgnoreUnknownCq:           true,
				DryRunStrategy:            dryrun.None,
				Client:                    fake.NewSimpleClientset().KueueV1beta2(),
			},
		},
		"unknown cluster queue with dry-run client succeeds without remote call": {
			options: &LocalQueueOptions{
				Name:                      "lq1",
				Namespace:                 "ns1",
				ClusterQueue:              "unknown-cq",
				UserSpecifiedClusterQueue: "unknown-cq",
				DryRunStrategy:            dryrun.Client,
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := tc.options.Validate(context.Background())
			var gotErrStr string
			if err != nil {
				gotErrStr = err.Error()
			}
			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got):\n%s", diff)
			}
		})
	}
}
