package main

import (
	"log"
	"os"

	"sigs.k8s.io/kueue/internal/tools/yaml-processor/yamlproc"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: ./yaml-processor <processing-plan.yaml>")
	}

	planPath := os.Args[1]
	processingPlan, err := yamlproc.LoadProcessingPlan(planPath)
	if err != nil {
		log.Fatalf("Failed to load processing plan: %v", err)
	}

	yqClient := yamlproc.NewYQClient()
	textInserter := yamlproc.NewTextInserter(yqClient)
	fileProcessor := yamlproc.NewProcessor(yqClient, textInserter)

	fileProcessor.ProcessPlan(*processingPlan)
}
