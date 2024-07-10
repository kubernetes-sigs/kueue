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
)

const (
	defaultListRequestLimit        = 100
	KjobctlListRequestLimitEnvName = "KJOBCTL_LIST_REQUEST_LIMIT"
)

var (
	invalidListRequestLimitError = errors.New("invalid list request limit")
)

func listRequestLimit() (int64, error) {
	listRequestLimitEnv := os.Getenv(KjobctlListRequestLimitEnvName)

	if len(listRequestLimitEnv) == 0 {
		return defaultListRequestLimit, nil
	}

	limit, err := strconv.ParseInt(listRequestLimitEnv, 10, 64)
	if err != nil {
		return 0, invalidListRequestLimitError
	}

	return limit, nil
}
