package utils

import (
	"context"
	"fmt"
)

type FakeRayHttpProxyClient struct {
	IsHealthy bool
}

func (fc *FakeRayHttpProxyClient) CheckProxyActorHealth(_ context.Context) error {
	if !fc.IsHealthy {
		return fmt.Errorf("fake proxy actor is not healthy")
	}
	return nil
}
