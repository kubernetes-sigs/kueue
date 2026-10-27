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

package e2e

import "sync"

var (
	// For full documentation on agnhost subcommands see the following documentation:
	// https://pkg.go.dev/k8s.io/kubernetes/test/images/agnhost#section-readme

	// Starts a simple HTTP(S) with a few endpoints, one of which is the /exit endpoint which exits with `exit 0`
	BehaviorWaitForDeletion = []string{"netexec"}

	// Starts a container which always ends in failure on deletion.
	// To achieve this runs simple webserver, but does not register any signal handler.
	BehaviorWaitForDeletionFailOnExit = []string{"test-webserver"}

	// The agnhost container will print args passed and `exit 0`
	BehaviorExitFast = []string{"entrypoint-tester"}
)

var (
	sparkTestImageOnce sync.Once
	sparkTestImage     string

	agnHostImageOnce sync.Once
	agnHostImage     string

	redisTestImageOnce sync.Once
	redisTestImage     string
)

const (
	Shard0 = "shard-0"
	Shard1 = "shard-1"
)
