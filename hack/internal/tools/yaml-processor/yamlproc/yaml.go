package yamlproc

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func RemoveDocumentSeparators(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	var filtered []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "---" {
			filtered = append(filtered, line)
		}
	}

	return []byte(strings.Join(filtered, "\n"))
}

// Sanitize processes the given YAML data and ensures it is valid by replacing invalid lines
// with dummy key-value pairs. This allows tools like YQ to operate on the file without errors.
// - Multiline blocks are preserved.
// - Invalid lines are replaced with "dummy: placeholder" to maintain structure.
func Sanitize(yamlData []byte) []byte {
	var inMultilineBlock, inInvalidBlock bool
	var currentIndentation, invalidBlockIndent string
	var validYAML strings.Builder

	lines := strings.Split(string(yamlData), "\n")
	for _, line := range lines {
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

	var parsed interface{}
	err := yaml.Unmarshal([]byte(line), &parsed)
	return err == nil
}

func YAMLToJSON(yamlStr string) (string, error) {
	var data interface{}
	err := yaml.Unmarshal([]byte(yamlStr), &data)
	if err != nil {
		return "", err
	}
	data = convertToStringKeys(data)

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}

	return string(jsonBytes), nil
}

func convertToStringKeys(i interface{}) interface{} {
	switch v := i.(type) {
	case map[interface{}]interface{}:
		newMap := make(map[string]interface{})
		for key, value := range v {
			strKey := fmt.Sprintf("%v", key) // Convert key to string
			newMap[strKey] = convertToStringKeys(value)
		}
		return newMap
	case []interface{}:
		for i, item := range v {
			v[i] = convertToStringKeys(item)
		}
	}
	return i
}
