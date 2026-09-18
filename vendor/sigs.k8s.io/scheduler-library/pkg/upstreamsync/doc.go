// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package upstreamsync holds the logic that is duplicated from, or destined for, the upstream
// kube-scheduler. It is not the library's entry point: consumers should start from
// sigs.k8s.io/scheduler-library/pkg/simulator.
//
// Everything here is temporary debt, kept in one place so that it is easy to see what has to be
// merged back into kubernetes/kubernetes, as described in CONTRIBUTING.md.
//
// # Marking the divergences from upstream
//
// Code copied from kube-scheduler keeps the upstream doc comments verbatim, so that a copy can be
// diffed against its origin. Every intentional deviation is called out with a comment starting
// with the "UPSTREAM-DIFF:" prefix, placed either on the declaration (when the whole function
// differs) or right at the line that differs. Grepping for the prefix lists everything that has to
// be reconciled before the code can be upstreamed:
//
//	grep -rn "UPSTREAM-DIFF:" pkg/
//
// Each such comment states what changed and why, e.g. that the library has no scheduling queue,
// so the nomination handling was dropped.
package upstreamsync
