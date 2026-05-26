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
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
	"go.uber.org/zap"
	glog "gopkg.in/op/go-logging.v1"
	"sigs.k8s.io/yaml"
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

func (yq *YQClient) Insert(data []byte, key, value, onItemCondition string) ([]byte, error) {
	jsonValue, err := yaml.YAMLToJSON([]byte(value))
	if err != nil {
		return nil, err
	}

	selectExp := yq.buildSelectExpression(key, onItemCondition)
	expression := fmt.Sprintf("(%s) |= %s + .", selectExp, jsonValue)
	return yq.Evaluate(data, expression)
}

func (yq *YQClient) Append(data []byte, key, value, onItemCondition string) ([]byte, error) {
	selectExp := yq.buildSelectExpression(key, onItemCondition)
	expression := fmt.Sprintf("(%s) |= %s + .", selectExp, value)

	return yq.Evaluate(data, expression)
}

func (yq *YQClient) Update(data []byte, key, value, onItemCondition string) ([]byte, error) {
	selectExp := yq.buildSelectExpression(key, onItemCondition)
	expression := fmt.Sprintf("(%s) = %s", selectExp, value)

	return yq.Evaluate(data, expression)
}

func (yq *YQClient) DeleteKey(data []byte, key, onItemCondition string) ([]byte, error) {
	selectExp := yq.buildSelectExpression(key, onItemCondition)
	expression := fmt.Sprintf("del(%s)", selectExp)

	return yq.Evaluate(data, expression)
}

func (yq *YQClient) buildSelectExpression(key, condition string) string {
	if condition == "" {
		return key
	}

	baseArrayKey := strings.Split(key, "[]")[0] + "[]"
	remainingKey := strings.TrimPrefix(key, baseArrayKey)
	condition = strings.TrimPrefix(condition, baseArrayKey)

	return fmt.Sprintf("%s | select(%s) | %s", baseArrayKey, condition, remainingKey)
}

func (yq *YQClient) HasKey(data []byte, key string) (bool, error) {
	if !strings.HasPrefix(key, ".") {
		key = "." + key
	}

	expression := yq.buildHasExpression(key)
	out, err := yq.Evaluate(data, expression)
	if err != nil {
		logger.Debug("Cannot run yq expression", zap.String("expression", expression), zap.Error(err))
		return false, err
	}

	val := strings.TrimSpace(string(out))

	return slices.Contains(strings.Split(val, "\n"), "true"), nil
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

func (yq *YQClient) EvaluateCondition(data []byte, condition string) (bool, error) {
	result, err := yq.Evaluate(data, condition)
	if err != nil {
		logger.Debug("Cannot evaluate condition", zap.String("condition", condition), zap.Error(err))
		return false, err
	}

	return strings.TrimSpace(string(result)) == "true", nil
}

// FindKeyLines returns the zero-based line numbers where the specified key is located in the YAML.
func (yq *YQClient) FindKeyLines(data []byte, key, condition string) ([]int, error) {
	selectExp := yq.buildSelectExpression(key, condition)
	expression := fmt.Sprintf("%s | key | line", selectExp)

	return yq.extractLineNumbers(data, expression)
}

func (yq *YQClient) extractLineNumbers(data []byte, expression string) ([]int, error) {
	result, err := yq.Evaluate(data, expression)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate expression '%s': %v", expression, err)
	}

	// Parse the line numbers from the result
	var lineNumbers []int
	for line := range strings.SplitSeq(strings.TrimSpace(string(result)), "\n") {
		lineNum, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("error parsing line number '%s': %w", line, err)
		}
		lineNumbers = append(lineNumbers, lineNum-1) // Convert to zero-based index
	}

	return lineNumbers, nil
}

// FormatYAML ensures the YAML has a consistent format and maintains uniformity across files.
func (yq *YQClient) FormatYAML(data []byte) ([]byte, error) {
	formattedData, err := yq.Evaluate(data, ".")
	if err != nil {
		return nil, err
	}

	return formattedData, nil
}
