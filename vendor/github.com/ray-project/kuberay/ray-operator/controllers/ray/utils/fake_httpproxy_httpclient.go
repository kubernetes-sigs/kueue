package utils

import (
	"fmt"
	"net/http"
	"time"
)

type FakeRayHttpProxyClient struct {
	client       http.Client
	httpProxyURL string
}

func (r *FakeRayHttpProxyClient) InitClient() {
	r.client = http.Client{
		Timeout: 20 * time.Millisecond,
	}
}

func (r *FakeRayHttpProxyClient) SetHostIp(hostIp string, port int) {
	r.httpProxyURL = fmt.Sprintf("http://%s:%d", hostIp, port)
}

func (r *FakeRayHttpProxyClient) CheckHealth() error {
	// TODO: test check return error cases.
	// Always return successful.
	return nil
}
