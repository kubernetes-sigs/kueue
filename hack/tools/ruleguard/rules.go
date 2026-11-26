//go:build ruleguard

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

// Package gorules holds the gocritic ruleguard rules that .golangci.yaml loads.
// golangci-lint does not invalidate its cache when this file changes, so run
// "golangci-lint cache clean" before you check a changed rule locally.
//
// ruleguard type-checks this file against every module that "make ci-lint"
// lints, so each of those modules needs a ruleguard-tagged import of
// github.com/quasilyte/go-ruleguard/dsl, as in hack/ruleguard/dsl.go.
// Without it, lint of that module fails to import the DSL.
package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

// mapTableTests reports table-driven tests that range over a slice and name
// subtests with a field of the case. Use a map keyed by the case name instead,
// with cases that do not depend on each other.
func mapTableTests(m dsl.Matcher) {
	m.Match(`for $_, $tc := range $cases { $*body }`).
		Where(m["cases"].Type.Underlying().Is(`[]$_`) &&
			(m["body"].Contains(`$t.Run($tc.$_, $*_)`) ||
				m["body"].Contains(`$t.Run($tc.$_.$_, $*_)`) ||
				m["body"].Contains(`$t.Run(fmt.Sprintf($*_, $tc.$_, $*_), $*_)`) ||
				m["body"].Contains(`$t.Run(fmt.Sprintf($*_, $tc.$_.$_, $*_), $*_)`))).
		Report(`use a map-based table keyed by the test case name instead of ranging over a slice and naming subtests with a field of $tc`)
}
