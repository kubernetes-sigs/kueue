package main

import (
	"log"

	"github.com/akram/kueue-viz-go/handlers"
	"github.com/gin-gonic/gin"

	"net/http"
	_ "net/http/pprof"
)

func main() {
	// Start pprof server for profiling
	go func() {
		log.Println("Starting pprof server on :6060")
		if err := http.ListenAndServe("localhost:6060", nil); err != nil {
			log.Fatalf("Error starting pprof server: %v", err)
		}
	}()

	k8sClient, dynamicClient, err := createK8sClient()
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}
	r := gin.New()
	r.Use(gin.Logger())
	r.SetTrustedProxies(nil)

	handlers.InitializeWebSocketRoutes(r, dynamicClient, k8sClient)

	if err := r.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

}
