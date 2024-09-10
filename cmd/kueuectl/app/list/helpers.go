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

package list

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	defaultListRequestLimit         = 100
	KueuectlListRequestLimitEnvName = "KUEUECTL_LIST_REQUEST_LIMIT"
)

var (
	invalidListRequestLimitError = errors.New("invalid list request limit")
)

func listRequestLimit() (int64, error) {
	listRequestLimitEnv := os.Getenv(KueuectlListRequestLimitEnvName)

	if len(listRequestLimitEnv) == 0 {
		return defaultListRequestLimit, nil
	}

	limit, err := strconv.ParseInt(listRequestLimitEnv, 10, 64)
	if err != nil {
		return 0, invalidListRequestLimitError
	}

	return limit, nil
}

func addFieldSelectorFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVar(p, "field-selector", "",
		"Selector (field query) to filter on, supports '=', '==', and '!='.(e.g. --field-selector key1=value1,key2=value2). The server only supports a limited number of field queries per type.")
}

func addLabelSelectorFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "selector", "l", "",
		"Selector (label query) to filter on, supports '=', '==', and '!='.(e.g. -l key1=value1,key2=value2). Matching objects must satisfy all of the specified label constraints.")
}

func addClusterQueueFilterFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "clusterqueue", "c", "",
		"Filter by cluster queue name which is associated with the resource.")
}

func addLocalQueueFilterFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "localqueue", "q", "",
		"Filter by local queue name which is associated with the resource.")
}

func addActiveFilterFlagVar(cmd *cobra.Command, p *[]bool) {
	cmd.Flags().BoolSliceVar(p, "active", make([]bool, 0),
		"Filter by active status. Valid values: 'true' and 'false'.")
}

func addForObjectFlagVar(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVar(p, "for", "",
		"Filter only those pertaining to the specified resource.")
}

// decodeResourceTypeName handles type/name resource formats and returns a resource tuple
// (empty or not), whether it successfully found one, and an error
// copied from https://github.com/kubernetes/kubernetes/blob/8565e375251450a291f0af3c6195c7a5bf890292/staging/src/k8s.io/kubectl/pkg/cmd/events/events.go#L380
func decodeResourceTypeName(mapper meta.RESTMapper, s string) (gvk schema.GroupVersionKind, name string, found bool, err error) {
	seg := strings.Split(s, "/")
	if len(seg) != 2 {
		if len(seg) > 2 {
			err = errors.New("arguments in resource/name form may not have more than one slash")
		}
		return
	}
	resource, name := seg[0], seg[1]

	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(strings.ToLower(resource))
	gvr := schema.GroupVersionResource{}
	if fullySpecifiedGVR != nil {
		gvr, _ = mapper.ResourceFor(*fullySpecifiedGVR)
	}
	if gvr.Empty() {
		gvr, err = mapper.ResourceFor(groupResource.WithVersion(""))
		if err != nil {
			return
		}
	}

	gvk, err = mapper.KindFor(gvr)
	if err != nil {
		return
	}
	found = true

	return
}
