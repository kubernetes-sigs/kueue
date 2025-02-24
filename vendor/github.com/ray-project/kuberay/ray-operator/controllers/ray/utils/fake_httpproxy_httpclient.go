package utils

import (
	"context"
	"fmt"
)

type FakeRayHttpProxyClient struct {
	IsHealthy bool
}

func (fc *FakeRayHttpProxyClient) InitClient() {}

func (fc *FakeRayHttpProxyClient) SetHostIp(_, _, _ string, _ int) {}

func (fc *FakeRayHttpProxyClient) CheckProxyActorHealth(_ context.Context) error {
	if !fc.IsHealthy {
		return fmt.Errorf("fake proxy actor is not healthy")
	}
	return nil
}
