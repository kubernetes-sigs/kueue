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
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type ProcessingPlan struct {
	Files []FileOperations `yaml:"files"`
}

type FileOperations struct {
	Path            string      `yaml:"path"`
	Excludes        []string    `yaml:"excludes,omitempty"`
	Operations      []Operation `yaml:"operations"`
	PostOperations  []Operation `yaml:"postOperations,omitempty"`
	OutputDir       string      `yaml:"outputDir,omitempty"`
	ContinueOnError bool        `yaml:"continueOnError,omitempty"`
}

type Operation struct {
	Type            string `yaml:"type"`
	Key             string `yaml:"key,omitempty"` // Key is optional for INSERT_TEXT
	Value           string `yaml:"value"`
	Indentation     int    `yaml:"indentation,omitempty"`
	AddKeyIfMissing bool   `yaml:"addKeyIfMissing,omitempty"`
	Position        string `yaml:"position,omitempty"` // Position is valid only for INSERT_TEXT
	OnFileCondition string `yaml:"onFileCondition,omitempty"`
	OnItemCondition string `yaml:"onItemCondition,omitempty"`
}

const (
	InsertObject = "INSERT_OBJECT"
	InsertText   = "INSERT_TEXT"
	Append       = "APPEND"
	Update       = "UPDATE"
	Delete       = "DELETE"
)

type FileProcessor struct {
	yq           *YQClient
	textInserter *TextInserter
}

var logger *zap.Logger

func SetLogger(l *zap.Logger) {
	logger = l
}

func NewProcessor(yq *YQClient, textInserter *TextInserter) *FileProcessor {
	return &FileProcessor{
		yq:           yq,
		textInserter: textInserter,
	}
}

func (fp *FileProcessor) ProcessPlan(plan ProcessingPlan) {
	for _, file := range plan.Files {
		outputDir := file.OutputDir
		if outputDir == "" {
			logger.Fatal("Missing outputDir for file", zap.String("file", file.Path))
		}

		matchedFiles, err := filepath.Glob(file.Path)
		if err != nil {
			logger.Fatal("Failed to expand glob pattern", zap.String("pattern", file.Path), zap.Error(err))
		}

		if len(matchedFiles) == 0 {
			logger.Warn("No files matched for pattern", zap.String("pattern", file.Path))
		}

		for _, filePath := range filterFiles(matchedFiles, file.Excludes) {
			file.Path = filePath
			err := fp.ProcessFile(file)
			if err != nil && !file.ContinueOnError {
				logger.Fatal("Failed to process file", zap.String("file", file.Path), zap.Error(err))
			}
		}
	}
}

func filterFiles(files, excludes []string) []string {
	var filtered []string
	for _, file := range files {
		if !isExcluded(file, excludes) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func isExcluded(file string, excludes []string) bool {
	base := filepath.Base(file)
	for _, pattern := range excludes {
		matched, err := filepath.Match(pattern, base)
		if err != nil {
			logger.Warn("Invalid exclude pattern", zap.String("pattern", file), zap.Error(err))
			continue
		}

		if matched {
			return true
		}
	}
	return false
}

func (fp *FileProcessor) ProcessFile(fileOps FileOperations) error {
	if fileOps.OutputDir != "" {
		if err := os.MkdirAll(fileOps.OutputDir, 0755); err != nil {
			logger.Error("Failed to create output directory", zap.String("outputDir", fileOps.OutputDir), zap.Error(err))
			return err
		}
	}

	var opErrors []error
	data, err := fp.prepareFile(fileOps)
	if err != nil {
		logger.Error("Skipping file due to preparation error", zap.String("file", fileOps.Path), zap.Error(err))
		return err
	}

	if isMultiDocumentYAML(data) {
		docs, err := SplitYAMLDocuments(data)
		if err != nil {
			logger.Error("Skipping file due to YAML splitting error", zap.String("file", fileOps.Path), zap.Error(err))
			return err
		}

		var modifiedDocs [][]byte
		for _, doc := range docs {
			modifiedDoc, errs := fp.ProcessFileOperations(doc, fileOps)
			modifiedDocs = append(modifiedDocs, modifiedDoc)
			opErrors = append(opErrors, errs...)
		}

		data = JoinYAMLDocuments(modifiedDocs)
	} else {
		data, opErrors = fp.ProcessFileOperations(data, fileOps)
	}

	if len(opErrors) > 0 && !fileOps.ContinueOnError {
		return errors.Join(opErrors...)
	}

	outputPath := filepath.Join(fileOps.OutputDir, filepath.Base(fileOps.Path))
	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		logger.Error("Failed to write file", zap.String("outputPath", outputPath), zap.Error(err))
		return err
	}

	return nil
}

func (fp *FileProcessor) ProcessFileOperations(data []byte, fileOps FileOperations) ([]byte, []error) {
	var opErrors []error

	data, regularOpErrors := fp.processRegularOperations(data, fileOps)
	opErrors = append(opErrors, regularOpErrors...)

	data, postOpErrors := fp.processPostOperations(data, fileOps)
	opErrors = append(opErrors, postOpErrors...)

	return data, opErrors
}

func (fp *FileProcessor) processRegularOperations(data []byte, fileOps FileOperations) ([]byte, []error) {
	var opErrors []error

	for _, op := range fileOps.Operations {
		if err := fp.validateOperation(op, data); err != nil {
			logger.Warn("Skipping operation", zap.String("operation", op.Type), zap.String("file", fileOps.Path), zap.Error(err))
			opErrors = append(opErrors, err)
			continue
		}

		var err error
		switch op.Type {
		case InsertObject:
			data, err = fp.yq.Insert(data, op.Key, op.Value, op.OnItemCondition)
		case Append:
			data, err = fp.yq.Append(data, op.Key, op.Value, op.OnItemCondition)
		case Update:
			data, err = fp.yq.Update(data, op.Key, op.Value, op.OnItemCondition)
		case Delete:
			data, err = fp.yq.DeleteKey(data, op.Key, op.OnItemCondition)
		default:
			logger.Warn("Unknown operation type", zap.String("operation", op.Type), zap.String("file", fileOps.Path))
			opErrors = append(opErrors, fmt.Errorf("unknown operation type: %s", op.Type))
			continue
		}

		if err != nil {
			logger.Warn("Cannot apply operation", zap.String("operation", op.Type), zap.Error(err))
			opErrors = append(opErrors, err)
		}
	}

	return data, opErrors
}

func (fp *FileProcessor) processPostOperations(data []byte, fileOps FileOperations) ([]byte, []error) {
	var opErrors []error
	var validOps []Operation

	// Validate all post operations beforehand, as INSERT_TEXT operations can potentially lead to invalid YAML.
	for _, op := range fileOps.PostOperations {
		if err := fp.validatePostOperation(op, data); err != nil {
			logger.Warn("Skipping post operation", zap.String("operation", op.Type), zap.String("file", fileOps.Path), zap.Error(err))
			opErrors = append(opErrors, err)
			continue
		}
		validOps = append(validOps, op)
	}

	for _, op := range validOps {
		var err error
		switch op.Type {
		case InsertText:
			data, err = fp.textInserter.Insert(data, InsertOptions{
				Key:             op.Key,
				Position:        op.Position,
				Value:           op.Value,
				Indentation:     op.Indentation,
				OnItemCondition: op.OnItemCondition,
			})
		default:
			logger.Warn("Unknown post operation type", zap.String("operation", op.Type), zap.String("file", fileOps.Path))
			opErrors = append(opErrors, fmt.Errorf("unknown post operation type: %s", op.Type))
			continue
		}

		if err != nil {
			logger.Warn("Cannot apply post operation", zap.String("operation", op.Type), zap.Error(err))
			opErrors = append(opErrors, err)
		}
	}

	return data, opErrors
}

func (fp *FileProcessor) prepareFile(fileOps FileOperations) ([]byte, error) {
	data, err := os.ReadFile(fileOps.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file '%s': %v", fileOps.Path, err)
	}

	data, err = fp.yq.FormatYAML(data)
	if err != nil {
		return nil, fmt.Errorf("failed to format YAML for file '%s': %v", fileOps.Path, err)
	}

	return data, nil
}

func (fp *FileProcessor) validateOperation(op Operation, data []byte) error {
	if op.OnFileCondition != "" && !fp.yq.EvaluateCondition(data, op.OnFileCondition) {
		return fmt.Errorf("condition '%s' not met", op.OnFileCondition)
	}

	if !fp.yq.HasKey(data, op.Key) && !op.AddKeyIfMissing {
		return fmt.Errorf("key '%s' does not exist", op.Key)
	}

	return nil
}

func (fp *FileProcessor) validatePostOperation(op Operation, data []byte) error {
	if op.OnFileCondition != "" && !fp.yq.EvaluateCondition(data, op.OnFileCondition) {
		return fmt.Errorf("condition '%s' not met", op.OnFileCondition)
	}

	if op.Type == InsertText {
		if op.Key == "" && op.Position == "" {
			return errors.New("either 'key' or 'position' must be specified")
		}
		if op.Key != "" && op.Position != "" {
			return errors.New("only one of 'key' or 'position' can be specified")
		}
	}

	return nil
}

func LoadProcessingPlan(path string) (*ProcessingPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var plan ProcessingPlan
	if err := yaml.Unmarshal(data, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}
