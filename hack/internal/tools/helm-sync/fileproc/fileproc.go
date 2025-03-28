package fileproc

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Files []FileConfig `yaml:"files"`
}

type FileConfig struct {
	Path             string      `yaml:"path"`
	Excludes         []string    `yaml:"excludes,omitempty"`
	Operations       []Operation `yaml:"operations"`
	OutputDir        string      `yaml:"output_dir,omitempty"`
	RemoveSeparators bool        `yaml:"remove_separators,omitempty"` // New flag to remove separators
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

func NewFileProcessor(yq *YQClient, textInserter *TextInserter) *FileProcessor {
	return &FileProcessor{
		yq:           yq,
		textInserter: textInserter,
	}
}

func (fp *FileProcessor) ProcessFile(config FileConfig) {
	if config.OutputDir != "" {
		if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
			log.Printf("Failed to create output directory %s: %v", config.OutputDir, err)
			return
		}
	}

	data, err := os.ReadFile(config.Path)
	if err != nil {
		log.Printf("Skipping %s: %v", config.Path, err)
		return
	}

	if config.RemoveSeparators {
		data, err = fp.removeDocumentSeparators(data)
		if err != nil {
			log.Printf("Error removing document separators: %v", err)
			return
		}
	}

	var textInsertions []InsertOptions
	for _, op := range config.Operations {
		if op.Type == InsertText {
			if op.Key == "" && op.Position == "" {
				log.Printf("Skipping InsertText operation: Either 'key' or 'position' must be specified")
				continue
			}

			if op.Key != "" && op.Position != "" {
				log.Printf("Skipping InsertText operation: Only one of 'key' or 'position' can be specified")
				continue
			}
		}

		if op.OnCondition != "" && !fp.yq.EvaluateCondition(data, op.OnCondition) {
			log.Printf("Skipping operation '%s': condition '%s' not met", op.Type, op.OnCondition)
			continue
		}

		if op.Type != InsertText && !fp.yq.HasKey(data, op.Key) && !op.AddKeyIfMissing {
			log.Printf("Skipping operation on key %s and file %s: Key does not exist", op.Key, config.Path)
			continue
		}

		switch op.Type {
		case InsertObject:
			data, err = fp.yq.Insert(data, op.Key, op.Value)
		case InsertText:
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
		data = fp.textInserter.Insert(data, opts)
	}

	outputPath := filepath.Join(config.OutputDir, filepath.Base(config.Path))
	if err := os.WriteFile(outputPath, []byte(data), 0644); err != nil {
		log.Printf("Failed to write file: %v", err)
	}
}

func (fp *FileProcessor) removeDocumentSeparators(content []byte) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	var filtered []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "---" {
			filtered = append(filtered, line)
		}
	}
	return []byte(strings.Join(filtered, "\n")), nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func FilterFiles(files, excludes []string) []string {
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
