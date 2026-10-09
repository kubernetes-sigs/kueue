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

package coverprofile

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// blockLine matches a coverage block: file:startLine.startCol,endLine.endCol numStmt count.
var blockLine = regexp.MustCompile(`^(.+):[0-9]+\.[0-9]+,[0-9]+\.[0-9]+ [0-9]+ [0-9]+$`)

// Filter drops coverage blocks whose file path has a directory segment named test.
// The mode line is kept. A non-empty line that is neither a mode line nor a
// coverage block is an error.
func Filter(profile string) (string, error) {
	var b strings.Builder
	b.Grow(len(profile))
	for line := range strings.SplitSeq(profile, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "mode:") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		file, ok := coverageFile(line)
		if !ok {
			return "", fmt.Errorf("malformed coverage profile line: %q", line)
		}
		if hasTestDir(file) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func coverageFile(line string) (string, bool) {
	m := blockLine.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// hasTestDir reports whether path has a slash-separated segment equal to test.
func hasTestDir(path string) bool {
	path = strings.ReplaceAll(path, `\`, "/")
	return slices.Contains(strings.Split(path, "/"), "test")
}
