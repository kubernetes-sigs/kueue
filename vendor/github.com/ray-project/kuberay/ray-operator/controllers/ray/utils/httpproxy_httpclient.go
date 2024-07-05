package utils

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

type RayHttpProxyClientInterface interface {
	InitClient()
	CheckHealth() error
	SetHostIp(hostIp string, port int)
}

func GetRayHttpProxyClient() RayHttpProxyClientInterface {
	return &RayHttpProxyClient{}
}

type RayHttpProxyClient struct {
	client       http.Client
	httpProxyURL string
}

func (r *RayHttpProxyClient) InitClient() {
	r.client = http.Client{
		Timeout: 20 * time.Millisecond,
	}
}

func (r *RayHttpProxyClient) SetHostIp(hostIp string, port int) {
	r.httpProxyURL = fmt.Sprintf("http://%s:%d/", hostIp, port)
}

func (r *RayHttpProxyClient) CheckHealth() error {
	req, err := http.NewRequest("GET", r.httpProxyURL+RayServeProxyHealthPath, nil)
	if err != nil {
		return err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("RayHttpProxyClient CheckHealth fail: %s %s", resp.Status, string(body))
	}

	return nil
}
