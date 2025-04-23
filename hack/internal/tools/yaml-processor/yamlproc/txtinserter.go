package yamlproc

import (
	"bytes"
	"slices"
	"strings"
)

const (
	PositionStart = "START"
	PositionEnd   = "END"
)

type TextInserter struct {
	yq *YQClient
}

type InsertOptions struct {
	Key             string
	Position        string
	Value           string
	Indentation     int
	OnItemCondition string
}

func NewTextInserter(yq *YQClient) *TextInserter {
	return &TextInserter{
		yq: yq,
	}
}

func (ti *TextInserter) Insert(yamlData []byte, opts InsertOptions) ([]byte, error) {
	if opts.Position == PositionStart {
		return append([]byte(opts.Value), yamlData...), nil
	}

	if opts.Position == PositionEnd {
		return append(yamlData, []byte(opts.Value)...), nil
	}

	return ti.insertBelowKey(yamlData, opts)
}

func (ti *TextInserter) insertBelowKey(yamlData []byte, opts InsertOptions) ([]byte, error) {
	var buffer bytes.Buffer
	var offset int

	sanitizedYaml := Sanitize(yamlData)
	keyLines, err := ti.yq.FindKeyLines(sanitizedYaml, opts.Key, opts.OnItemCondition)
	if err != nil {
		return nil, err
	}

	yamlLines := strings.Split(string(yamlData), "\n")
	for i, line := range yamlLines {
		trimmedLine := strings.TrimSpace(line)
		buffer.WriteString(line + "\n")

		if slices.Contains(keyLines, i+offset) {
			baseIndent := line[:strings.Index(line, trimmedLine)]
			indentedContent := ti.indentContent(opts.Value, baseIndent+strings.Repeat(" ", opts.Indentation))
			buffer.WriteString(indentedContent)
			offset += len(strings.Split(indentedContent, "\n"))
		}
	}

	return []byte(strings.TrimRight(buffer.String(), "\n") + "\n"), nil
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
