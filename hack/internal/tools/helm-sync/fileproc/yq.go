package fileproc

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
	glog "gopkg.in/op/go-logging.v1"
	"gopkg.in/yaml.v2"
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
	jsonValue, err := yq.yamlToJSON(value)
	if err != nil {
		return nil, err
	}

	expression := fmt.Sprintf("%s |= %s + .", key, jsonValue)
	return yq.Evaluate(data, expression)
}

func (yq *YQClient) yamlToJSON(yamlStr string) (string, error) {
	var data interface{}
	err := yaml.Unmarshal([]byte(yamlStr), &data)
	if err != nil {
		return "", err
	}
	data = yq.convertToStringKeys(data)

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}

	return string(jsonBytes), nil
}

func (yq *YQClient) convertToStringKeys(i interface{}) interface{} {
	switch v := i.(type) {
	case map[interface{}]interface{}:
		newMap := make(map[string]interface{})
		for key, value := range v {
			strKey := fmt.Sprintf("%v", key) // Convert key to string
			newMap[strKey] = yq.convertToStringKeys(value)
		}
		return newMap
	case []interface{}:
		for i, item := range v {
			v[i] = yq.convertToStringKeys(item)
		}
	}
	return i
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

	if strings.Contains(key, "[].") {
		parts := strings.Split(key, "[].")
		return fmt.Sprintf(".%s[] | has(\"%s\")", parts[0], parts[1])
	}

	parts := strings.Split(key, ".")
	if len(parts) == 1 {
		return fmt.Sprintf("has(\"%s\")", parts[0])
	}

	expression := fmt.Sprintf(".%s", parts[0])
	for i := 1; i < len(parts)-1; i++ {
		expression += fmt.Sprintf(".%s", parts[i])
	}
	expression += fmt.Sprintf(" | has(\"%s\")", parts[len(parts)-1])

	return expression
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
