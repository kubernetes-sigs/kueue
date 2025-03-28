package fileproc

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
)

const (
	PositionStart = "START"
	PositionEnd   = "END"
)

type TextInserter struct{}

type InsertOptions struct {
	Key         string
	Position    string
	Value       string
	Indentation int
}

func NewTextInserter() *TextInserter {
	return &TextInserter{}
}

func (ti *TextInserter) Insert(yamlData []byte, opts InsertOptions) []byte {
	if opts.Position == PositionStart {
		return append([]byte(opts.Value), yamlData...)
	}

	if opts.Position == PositionEnd {
		return append(yamlData, []byte(opts.Value)...)
	}

	yamlLines := strings.Split(string(yamlData), "\n")
	keyLines := ti.findKeyLines(opts.Key, yamlData)
	var offset int

	var buffer bytes.Buffer
	for i, line := range yamlLines {
		trimmedLine := strings.TrimSpace(line)
		buffer.WriteString(line + "\n")

		if slices.Contains(keyLines, i+1+offset) {
			baseIndent := line[:strings.Index(line, trimmedLine)]
			indentedContent := ti.indentContent(opts.Value, baseIndent+strings.Repeat(" ", opts.Indentation))
			buffer.WriteString(indentedContent)

			offset += len(strings.Split(indentedContent, "\n"))
		}
	}

	return []byte(strings.TrimRight(buffer.String(), "\n") + "\n")
}

func (ti *TextInserter) findKeyLines(key string, yamlData []byte) []int {
	var lineNumbers, indentStack []int
	var keyStack []string
	var lineNumber int

	keyParts := strings.Split(strings.TrimPrefix(key, "."), ".")

	scanner := bufio.NewScanner(bytes.NewReader(yamlData))
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		trimmedLine := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		for len(indentStack) > 0 && indentStack[len(indentStack)-1] >= indent {
			keyStack = keyStack[:len(keyStack)-1]
			indentStack = indentStack[:len(indentStack)-1]
		}

		if strings.Contains(trimmedLine, ":") {
			keyName := strings.SplitN(trimmedLine, ":", 2)[0]
			keyStack = append(keyStack, strings.TrimSpace(keyName))
			indentStack = append(indentStack, indent)

			if len(keyStack) == len(keyParts) && strings.Join(keyStack, ".") == strings.Join(keyParts, ".") {
				lineNumbers = append(lineNumbers, lineNumber)
			}
		}
	}

	return lineNumbers
}

func (ti *TextInserter) indentContent(content, indent string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}
