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
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type ProcessingPlan struct {
	GlobalOptions ProcessingPlanOptions `yaml:"globalOptions"`
	Files         []FileOperations      `yaml:"files"`
}

type ProcessingPlanOptions struct {
	BoilerplatePath string `yaml:"boilerplatePath"`
}

type FileOperations struct {
	Path            string      `yaml:"path"`
	Excludes        []string    `yaml:"excludes,omitempty"`
	Operations      []Operation `yaml:"operations"`
	PostOperations  []Operation `yaml:"postOperations,omitempty"`
	OutputDir       string      `yaml:"outputDir,omitempty"`
	ContinueOnError bool        `yaml:"continueOnError,omitempty"`
	RemoveComments  bool        `yaml:"removeComments,omitempty"`
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
			logger.Debug("No files matched for pattern", zap.String("pattern", file.Path))
		}

		filteredFiles, err := filterFiles(matchedFiles, file.Excludes)
		if err != nil {
			logger.Fatal("Failed to filter files", zap.Strings("excludes", file.Excludes), zap.Error(err))
		}

		for _, filePath := range filteredFiles {
			file.Path = filePath
			err := fp.ProcessFile(file, plan.GlobalOptions)
			if err != nil && !file.ContinueOnError {
				logger.Fatal("Failed to process file", zap.String("file", file.Path), zap.Error(err))
			}
		}
	}
}

func filterFiles(files, excludes []string) ([]string, error) {
	var filtered []string
	for _, file := range files {
		excluded, err := isExcluded(file, excludes)
		if err != nil {
			return nil, err
		}
		if !excluded {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}

func isExcluded(file string, excludes []string) (bool, error) {
	base := filepath.Base(file)
	for _, pattern := range excludes {
		matched, err := filepath.Match(pattern, base)
		if err != nil {
			logger.Debug("Invalid exclude pattern", zap.String("pattern", file), zap.Error(err))
			return false, err
		}

		if matched {
			return true, nil
		}
	}
	return false, nil
}

func (fp *FileProcessor) ProcessFile(fileOps FileOperations, options ProcessingPlanOptions) error {
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

	docs, err := SplitYAMLDocuments(data)
	if err != nil {
		logger.Error("Skipping file due to YAML splitting error", zap.String("file", fileOps.Path), zap.Error(err))
		return err
	}

	var modifiedDocs [][]byte
	for _, doc := range docs {
		if fileOps.RemoveComments {
			doc, err = RemoveComments(doc)
			if err != nil {
				logger.Error("Failed to remove comments", zap.String("file", fileOps.Path), zap.Error(err))
			}
		}
		modifiedDoc, errs := fp.ProcessFileOperations(doc, fileOps)
		modifiedDocs = append(modifiedDocs, modifiedDoc)
		opErrors = append(opErrors, errs...)
	}

	data = JoinYAMLDocuments(modifiedDocs)

	if len(opErrors) > 0 && !fileOps.ContinueOnError {
		return errors.Join(opErrors...)
	}

	outputPath := filepath.Join(fileOps.OutputDir, filepath.Base(fileOps.Path))

	prefix := fmt.Appendf(nil, "{{/* Code generated by %s. DO NOT EDIT. */}}\n\n", filepath.Base(os.Args[0]))

	if options.BoilerplatePath != "" {
		boilerplate, err := loadBoilerplate(options.BoilerplatePath)
		if err != nil {
			logger.Fatal("Failed to load boilerplate", zap.Error(err))
		}
		prefix = append(boilerplate, prefix...)
	}

	// Append prefix to data
	data = append(prefix, data...)

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
			logger.Debug("Skipping operation", zap.String("operation", op.Type), zap.String("file", fileOps.Path), zap.Error(err))
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
			logger.Debug("Unknown operation type", zap.String("operation", op.Type), zap.String("file", fileOps.Path))
			opErrors = append(opErrors, fmt.Errorf("unknown operation type: %s", op.Type))
			continue
		}

		if err != nil {
			logger.Debug("Cannot apply operation", zap.String("operation", op.Type), zap.Error(err))
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
			logger.Debug("Skipping post operation", zap.String("operation", op.Type), zap.String("file", fileOps.Path), zap.Error(err))
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
			logger.Debug("Unknown post operation type", zap.String("operation", op.Type), zap.String("file", fileOps.Path))
			opErrors = append(opErrors, fmt.Errorf("unknown post operation type: %s", op.Type))
			continue
		}

		if err != nil {
			logger.Debug("Cannot apply post operation", zap.String("operation", op.Type), zap.Error(err))
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
	met, err := fp.yq.EvaluateCondition(data, op.OnFileCondition)
	if err != nil {
		return err
	}

	if op.OnFileCondition != "" && !met {
		return fmt.Errorf("condition '%s' not met", op.OnFileCondition)
	}

	exist, err := fp.yq.HasKey(data, op.Key)
	if err != nil {
		return err
	}

	if !exist && !op.AddKeyIfMissing {
		return fmt.Errorf("key '%s' does not exist", op.Key)
	}

	return nil
}

func (fp *FileProcessor) validatePostOperation(op Operation, data []byte) error {
	met, err := fp.yq.EvaluateCondition(data, op.OnFileCondition)
	if err != nil {
		return err
	}

	if op.OnFileCondition != "" && !met {
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

func loadBoilerplate(path string) ([]byte, error) {
	if path != "" {
		boilerplate, err := os.ReadFile(path)
		if err != nil {
			logger.Fatal("Failed to load boilerplate", zap.Error(err))
		}

		// The boilerplate is expected to be a Go-style block comment: /* ... */
		// We wrap it as a Go-template action so it doesn't affect YAML parsing.
		//
		// Trimming whitespace is critical: otherwise the boilerplate typically ends
		// with "*/\n", which would terminate the comment before the template action
		// closes (leading to "comment ends before closing delimiter").
		boilerplate = bytes.TrimSpace(boilerplate)
		boilerplate = append([]byte("{{- "), boilerplate...)
		return append(boilerplate, []byte(" -}}\n\n")...), nil
	}
	return nil, nil
}
