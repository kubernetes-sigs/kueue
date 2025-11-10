package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type RayHttpProxyClientInterface interface {
	CheckProxyActorHealth(ctx context.Context) error
}

type RayHttpProxyClient struct {
	client       *http.Client
	httpProxyURL string
}

func (r *RayHttpProxyClient) InitClient() {
	r.client = &http.Client{
		Timeout: 2 * time.Second,
	}
}

// CheckProxyActorHealth checks the health status of the Ray Serve proxy actor.
func (r *RayHttpProxyClient) CheckProxyActorHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.httpProxyURL+RayServeProxyHealthPath, nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("CheckProxyActorHealth fails. status code: %d, status: %s, error reading body: %w", resp.StatusCode, resp.Status, err)
		}
		err = fmt.Errorf("CheckProxyActorHealth fails. status code: %d, status: %s, body: %s", resp.StatusCode, resp.Status, string(body))
		return err
	}
	// For responses with status code 200, we don't need to allocate memory for the response body.
	// Instead, we discard the contents directly to avoid unnecessary memory allocations.
	_, _ = io.Copy(io.Discard, resp.Body)

	return nil
}
