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

package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

func silencedContext(m dsl.Matcher) {
	m.Match(`_ = $ctx`, `var _ = $ctx`).
		Where(m["ctx"].Type.Is("context.Context")).
		Report("use the context for logging or context-aware calls; if required by an interface but unused, name the parameter _")
}
