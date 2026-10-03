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

package util

import (
	"errors"
	"os"
	"strconv"
)

const (
	DefaultListRequestLimit         = 100
	KueuectlListRequestLimitEnvName = "KUEUECTL_LIST_REQUEST_LIMIT"
)

// ErrInvalidListRequestLimit is returned when KUEUECTL_LIST_REQUEST_LIMIT is not an int.
var ErrInvalidListRequestLimit = errors.New("invalid list request limit")

// ListRequestLimit returns the page size for kueuectl list/delete List calls.
// It uses KUEUECTL_LIST_REQUEST_LIMIT when set, otherwise DefaultListRequestLimit.
func ListRequestLimit() (int64, error) {
	listRequestLimitEnv := os.Getenv(KueuectlListRequestLimitEnvName)

	if len(listRequestLimitEnv) == 0 {
		return DefaultListRequestLimit, nil
	}

	limit, err := strconv.ParseInt(listRequestLimitEnv, 10, 64)
	if err != nil {
		return 0, ErrInvalidListRequestLimit
	}

	return limit, nil
}
