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

package parser

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/utils/ptr"
)

var (
	invalidTimeFormatErr = errors.New("invalid time format")
)

// isValidTimeSpec validates that time format follows is supported.
// Inspired by https://github.com/SchedMD/slurm/blob/9322d01b1aadde28181254499dd0d80638a3df89/src/common/parse_time.c#L87-L145.
func isValidTimeSpec(val string) bool {
	var digit, dash, colon int

	var alreadyDigit bool
	for _, ch := range val {
		switch {
		case ch >= '0' && ch <= '9':
			if !alreadyDigit {
				digit++
				alreadyDigit = true
			}
		case ch == '-':
			alreadyDigit = false
			dash++
			if colon > 0 {
				return false
			}
		case ch == ':':
			alreadyDigit = false
			colon++
		default:
			return false
		}
	}

	if digit == 0 {
		return false
	}

	if dash > 1 || colon > 2 {
		return false
	}

	if dash > 0 {
		if colon == 1 && digit > 3 {
			return false
		}

		if colon == 2 && digit < 4 {
			return false
		}
	} else {
		if colon == 1 && digit < 2 {
			return false
		}

		if colon == 2 && digit < 3 {
			return false
		}
	}

	return true
}

// TimeLimitToSeconds converts a string to an equivalent time value.
// If val is empty, it returns nil. If format is invalid, it returns
// invalidTimeFormatErr.
//
// Possible formats:
// - "minutes"
// - "minutes:seconds"
// - "hours:minutes:seconds"
// - "days-hours"
// - "days-hours:minutes"
// - "days-hours:minutes:seconds"
//
// For more details, see https://slurm.schedmd.com/sbatch.html#OPT_time.
//
// Inspired by https://github.com/SchedMD/slurm/blob/9322d01b1aadde28181254499dd0d80638a3df89/src/common/parse_time.c#L731-L784
func TimeLimitToSeconds(val string) (*int32, error) {
	val = strings.TrimSpace(val)

	if val == "" {
		return nil, nil
	}

	if !isValidTimeSpec(val) {
		return nil, invalidTimeFormatErr
	}

	var d, h, m, s int32

	if strings.Contains(val, "-") {
		// days-[hours[:minutes[:seconds]]]
		// nolint:errcheck
		fmt.Sscanf(val, "%d-%d:%d:%d", &d, &h, &m, &s)
		d *= 86400
		h *= 3600
		m *= 60
	} else {
		// nolint:errcheck
		if n, _ := fmt.Sscanf(val, "%d:%d:%d", &h, &m, &s); n == 3 {
			// hours:minutes:seconds
			h *= 3600
			m *= 60
		} else {
			// minutes[:seconds]
			// h is minutes here and m is seconds due to sscanf parsing left to right
			s = m
			m = h * 60
			h = 0
		}
	}

	return ptr.To(d + h + m + s), nil
}
