package yamlproc

import (
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
	glog "gopkg.in/op/go-logging.v1"
)

type YQClient struct {
	evaluator yqlib.StringEvaluator
	encoder   yqlib.Encoder
	decoder   yqlib.Decoder
}

func NewYQClient() *YQClient {
	glog.SetLevel(glog.WARNING, "yq-lib")

	return &YQClient{
		evaluator: yqlib.NewStringEvaluator(),
		encoder:   yqlib.NewYamlEncoder(yqlib.ConfiguredYamlPreferences),
		decoder:   yqlib.NewYamlDecoder(yqlib.ConfiguredYamlPreferences),
	}
}

func (yq *YQClient) Evaluate(data []byte, expression string) ([]byte, error) {
	result, err := yq.evaluator.Evaluate(expression, string(data), yq.encoder, yq.decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to process YAML: %w", err)
	}
	return []byte(result), nil
}

func (yq *YQClient) Insert(data []byte, key, value string) ([]byte, error) {
	jsonValue, err := YAMLToJSON(value)
	if err != nil {
		return nil, err
	}

	expression := fmt.Sprintf("%s |= %s + .", key, jsonValue)
	return yq.Evaluate(data, expression)
}

func (yq *YQClient) Append(data []byte, key, value string) ([]byte, error) {
	expression := fmt.Sprintf("%s |= %s + .", key, value)

	return yq.Evaluate(data, expression)
}

func (yq *YQClient) Update(data []byte, key, value string) ([]byte, error) {
	expression := fmt.Sprintf("%s = %s", key, value)

	return yq.Evaluate(data, expression)
}

func (yq *YQClient) HasKey(data []byte, key string) bool {
	if !strings.HasPrefix(key, ".") {
		key = "." + key
	}

	expression := yq.buildHasExpression(key)
	out, err := yq.Evaluate(data, expression)
	if err != nil {
		log.Printf("Error running yq expression '%s': %v", expression, err)
		return false
	}

	val := strings.TrimSpace(string(out))
	if val == "" {
		return false
	}

	return slices.Contains(strings.Split(val, "\n"), "true")
}

func (yq *YQClient) buildHasExpression(key string) string {
	key = strings.TrimPrefix(key, ".")

	parts := strings.Split(key, ".")
	if len(parts) == 1 {
		return fmt.Sprintf("has(\"%s\")", parts[0])
	}

	lastElement := parts[len(parts)-1]
	prefix := strings.Join(parts[:len(parts)-1], ".")

	return fmt.Sprintf(".%s | has(\"%s\")", prefix, lastElement)
}

func (yq *YQClient) EvaluateCondition(data []byte, condition string) bool {
	result, err := yq.Evaluate(data, condition)
	if err != nil {
		log.Printf("Error evaluating condition '%s': %v", condition, err)
		return false
	}

	return strings.TrimSpace(string(result)) == "true"
}

func (yq *YQClient) DeleteKey(data []byte, key string) ([]byte, error) {
	expression := fmt.Sprintf("del(%s)", key)

	return yq.Evaluate(data, expression)
}

// FindKeyLines returns the zero-based line numbers where the specified key is located in the YAML.
func (yq *YQClient) FindKeyLines(data []byte, key string) ([]int, error) {
	expression := fmt.Sprintf("%s | key | line", key)

	result, err := yq.Evaluate(data, expression)
	if err != nil {
		return nil, fmt.Errorf("error finding key lines for '%s': %w", key, err)
	}

	var lineNumbers []int
	for _, line := range strings.Split(strings.TrimSpace(string(result)), "\n") {
		lineNum, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("error parsing line number '%s': %w", line, err)
		}
		lineNumbers = append(lineNumbers, lineNum-1)
	}

	return lineNumbers, nil
}

// FormatYAML ensures the YAML has a consistent format and maintains uniformity across files.
func (yq *YQClient) FormatYAML(data []byte) ([]byte, error) {
	formattedData, err := yq.Evaluate(data, ".")
	if err != nil {
		return nil, fmt.Errorf("formatting error: %w", err)
	}

	return formattedData, nil
}
