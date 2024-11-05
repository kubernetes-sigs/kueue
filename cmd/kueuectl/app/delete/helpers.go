/*
Copyright 2024 The Kubernetes Authors.

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

package delete

import (
	"fmt"
	"io"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

func printWithDryRunStrategy(out io.Writer, name string, dryRunStrategy util.DryRunStrategy) {
	switch dryRunStrategy {
	case util.DryRunClient:
		fmt.Fprintf(out, "%s deleted (client dry run)\n", name)
	case util.DryRunServer:
		fmt.Fprintf(out, "%s deleted (server dry run)\n", name)
	default:
		fmt.Fprintf(out, "%s deleted\n", name)
	}
}
