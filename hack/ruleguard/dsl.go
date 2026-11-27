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

// The gocritic ruleguard checker in .golangci.yaml type-checks
// hack/tools/ruleguard/rules.go against each linted module, so the module must
// require the ruleguard DSL. This import keeps that requirement through
// "go mod tidy"; the ruleguard build tag keeps it out of builds.
package ruleguard

import _ "github.com/quasilyte/go-ruleguard/dsl"
