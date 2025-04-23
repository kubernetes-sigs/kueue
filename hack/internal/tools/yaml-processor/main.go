package main

import (
	"log"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/kueue/internal/tools/yaml-processor/yamlproc"
)

func main() {
	logger, err := newLogger()
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	yamlproc.SetLogger(logger)

	if len(os.Args) < 2 {
		logger.Fatal("Usage: ./yaml-processor <processing-plan.yaml>")
	}

	planPath := os.Args[1]
	processingPlan, err := yamlproc.LoadProcessingPlan(planPath)
	if err != nil {
		logger.Fatal("Failed to load processing plan", zap.Error(err))
	}

	yqClient := yamlproc.NewYQClient()
	textInserter := yamlproc.NewTextInserter(yqClient)
	fileProcessor := yamlproc.NewProcessor(yqClient, textInserter)

	fileProcessor.ProcessPlan(*processingPlan)
}

func newLogger() (*zap.Logger, error) {
	loggerConfig := zap.NewProductionConfig()
	loggerConfig.Encoding = "console"
	loggerConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return loggerConfig.Build()
}
