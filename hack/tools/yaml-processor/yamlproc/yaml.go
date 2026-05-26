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

package yamlproc

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

func RemoveComments(data []byte) ([]byte, error) {
	// Decode YAML.
	var node yaml.Node
	err := yaml.Unmarshal(data, &node)
	if err != nil {
		return nil, err
	}

	// Remove comments.
	removeComments(&node)

	// Encode back to YAML.
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2) // Optional: Set indentation
	err = encoder.Encode(&node)
	if err != nil {
		return nil, err
	}

	return output.Bytes(), nil
}

// removeComments recursively clears comments from a YAML node and its children
func removeComments(node *yaml.Node) {
	if node == nil {
		return
	}
	node.HeadComment = ""
	node.LineComment = ""
	node.FootComment = ""
	for _, child := range node.Content {
		removeComments(child)
	}
}

func SplitYAMLDocuments(data []byte) ([][]byte, error) {
	var result [][]byte

	reader := bytes.NewReader(data)
	dec := yaml.NewDecoder(reader)
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			return result, nil
		}

		if err != nil {
			return nil, err
		}

		doc, err := yaml.Marshal(&node)
		if err != nil {
			return nil, err
		}
		result = append(result, doc)
	}
}

func JoinYAMLDocuments(docs [][]byte) []byte {
	var stringDocs []string
	for _, doc := range docs {
		stringDocs = append(stringDocs, string(doc))
	}

	return []byte(strings.Join(stringDocs, "---\n"))
}

// Sanitize processes the given YAML data and ensures it is valid by replacing invalid lines
// with dummy key-value pairs. This allows tools like YQ to operate on the file without errors.
// - Multiline blocks are preserved.
// - Invalid lines are replaced with "dummy: placeholder" to maintain structure.
func Sanitize(yamlData []byte) []byte {
	var inMultilineBlock, inInvalidBlock bool
	var currentIndentation, invalidBlockIndent string
	var validYAML strings.Builder

	lines := strings.SplitSeq(string(yamlData), "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)

		if inMultilineBlock {
			if isPartOfMultilineBlock(line, currentIndentation, trimmed) {
				validYAML.WriteString(line + "\n")
				continue
			}
			inMultilineBlock = false
			currentIndentation = ""
		}

		if startsMultilineBlock(trimmed) {
			inMultilineBlock = true
			currentIndentation = line[:len(line)-len(trimmed)]
			validYAML.WriteString(line + "\n")
			continue
		}

		if isValidYAMLLine(trimmed) {
			if inInvalidBlock {
				inInvalidBlock = false
				invalidBlockIndent = ""
			}
			validYAML.WriteString(line + "\n")
		} else {
			if !inInvalidBlock {
				inInvalidBlock = true
				invalidBlockIndent = line[:len(line)-len(trimmed)]
			}
			validYAML.WriteString(invalidBlockIndent + "dummy: placeholder\n")
		}
	}

	return []byte(validYAML.String())
}

func isPartOfMultilineBlock(line, currentIndentation, trimmed string) bool {
	return len(line) > len(currentIndentation) && strings.HasPrefix(line, currentIndentation) || line == currentIndentation || trimmed == ""
}

func startsMultilineBlock(trimmed string) bool {
	return strings.HasSuffix(trimmed, "|") || strings.HasSuffix(trimmed, "|-")
}

func isValidYAMLLine(line string) bool {
	if line == "" || strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "---" {
		return false
	}

	var parsed any
	err := yaml.Unmarshal([]byte(line), &parsed)
	return err == nil
}
