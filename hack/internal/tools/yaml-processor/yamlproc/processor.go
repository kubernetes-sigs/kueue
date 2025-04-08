package yamlproc

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ProcessingPlan struct {
	Files []FileOperations `yaml:"files"`
}

type FileOperations struct {
	Path             string      `yaml:"path"`
	Excludes         []string    `yaml:"excludes,omitempty"`
	Operations       []Operation `yaml:"operations"`
	OutputDir        string      `yaml:"output_dir,omitempty"`
	RemoveSeparators bool        `yaml:"remove_separators,omitempty"`
	Multidoc         bool        `yaml:"multidoc,omitempty"`
}

type Operation struct {
	Type            string `yaml:"type"`
	Key             string `yaml:"key,omitempty"` // Key is optional for INSERT_TEXT
	Value           string `yaml:"value"`
	Indentation     int    `yaml:"indentation,omitempty"`
	AddKeyIfMissing bool   `yaml:"add_key_if_missing,omitempty"`
	OnCondition     string `yaml:"on_condition,omitempty"`
	Position        string `yaml:"position,omitempty"` // Position is valid only for INSERT_TEXT
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
			log.Fatalf("Missing outputDir for file: %s", file.Path)
		}

		matchedFiles, err := filepath.Glob(file.Path)
		if err != nil {
			log.Fatalf("Failed to expand glob pattern: %v", err)
		}

		if len(matchedFiles) == 0 {
			log.Printf("No files matched for pattern: %s", file.Path)
		}

		for _, filePath := range filterFiles(matchedFiles, file.Excludes) {
			file.Path = filePath
			fp.ProcessFile(file)
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
			log.Printf("Warning: Invalid exclude pattern '%s': %v", pattern, err)
			continue
		}

		if matched {
			return true
		}
	}
	return false
}

func (fp *FileProcessor) ProcessFile(fileOps FileOperations) {
	if fileOps.OutputDir != "" {
		if err := os.MkdirAll(fileOps.OutputDir, 0755); err != nil {
			log.Printf("Failed to create output directory %s: %v", fileOps.OutputDir, err)
			return
		}
	}

	data, err := fp.prepareFile(fileOps)
	if err != nil {
		log.Printf("Skipping %s: %v", fileOps.Path, err)
		return
	}

	if fileOps.Multidoc {
		docs := SplitYAMLDocuments(data)

		var modifiedDocs [][]byte
		for _, doc := range docs {
			modifiedDoc := fp.ApplyOperations(doc, fileOps)
			modifiedDocs = append(modifiedDocs, modifiedDoc)
		}

		data = JoinYAMLDocuments(modifiedDocs)
	} else {
		data = fp.ApplyOperations(data, fileOps)
	}

	outputPath := filepath.Join(fileOps.OutputDir, filepath.Base(fileOps.Path))
	if err := os.WriteFile(outputPath, []byte(data), 0644); err != nil {
		log.Printf("Failed to write file: %v", err)
	}
}

func (fp *FileProcessor) ApplyOperations(data []byte, fileOps FileOperations) []byte {
	var textInsertions []InsertOptions
	var err error
	for _, op := range fileOps.Operations {
		if err := fp.validateOperation(op, data); err != nil {
			log.Printf("Skipping operation '%s' on file '%s': %v", op.Type, fileOps.Path, err)
			continue
		}

		switch op.Type {
		case InsertObject:
			data, err = fp.yq.Insert(data, op.Key, op.Value)
		case InsertText:
			// Allows inserting plain text that is not valid YAML content.
			// Since YQ is used for all other operations, INSERT_TEXT is executed at the end
			// to avoid invalidating the YAML file and blocking YQ operations.
			textInsertions = append(textInsertions, InsertOptions{
				Key:         op.Key,
				Position:    op.Position,
				Value:       op.Value,
				Indentation: op.Indentation,
			})
		case Append:
			data, err = fp.yq.Append(data, op.Key, op.Value)
		case Update:
			data, err = fp.yq.Update(data, op.Key, op.Value)
		case Delete:
			data, err = fp.yq.DeleteKey(data, op.Key)
		}

		if err != nil {
			log.Printf("Error processing operation '%s': %v", op.Type, err)
		}
	}

	for _, opts := range textInsertions {
		data, err = fp.textInserter.Insert(data, opts)
		if err != nil {
			log.Printf("Error processing operation '%s': %v", InsertText, err)
		}
	}

	return data
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

	if fileOps.RemoveSeparators {
		data = RemoveDocumentSeparators(data)
	}

	return data, nil
}

func (fp *FileProcessor) validateOperation(op Operation, data []byte) error {
	if op.Type == InsertText {
		if op.Key == "" && op.Position == "" {
			return fmt.Errorf("either 'key' or 'position' must be specified")
		}
		if op.Key != "" && op.Position != "" {
			return fmt.Errorf("only one of 'key' or 'position' can be specified")
		}
	}

	if op.OnCondition != "" && !fp.yq.EvaluateCondition(data, op.OnCondition) {
		return fmt.Errorf("condition '%s' not met", op.OnCondition)
	}

	if op.Type != InsertText && !fp.yq.HasKey(data, op.Key) && !op.AddKeyIfMissing {
		return fmt.Errorf("key '%s' does not exist", op.Key)
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
