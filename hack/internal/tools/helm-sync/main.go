package main

import (
	"log"
	"os"
	"path/filepath"

	"sigs.k8s.io/kueue/internal/tools/helm-sync/fileproc"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: go run main.go <config.yaml>")
	}

	configPath := os.Args[1]
	config, err := fileproc.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	yqClient := fileproc.NewYQClient()
	textInserter := fileproc.NewTextInserter()
	fileProcessor := fileproc.NewFileProcessor(yqClient, textInserter)
	for _, file := range config.Files {
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

		for _, filePath := range fileproc.FilterFiles(matchedFiles, file.Excludes) {
			fileProcessor.ProcessFile(fileproc.FileConfig{
				Path:             filePath,
				Excludes:         file.Excludes,
				Operations:       file.Operations,
				OutputDir:        file.OutputDir,
				RemoveSeparators: file.RemoveSeparators,
			})
		}
	}
}
