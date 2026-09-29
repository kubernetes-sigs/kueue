/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package generator

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	"sigs.k8s.io/kueue/test/performance/framework/controllers"
)

// DRAConfig represents the DRA device configuration from YAML
type DRAConfig struct {
	DevicesPerNode int `json:"devicesPerNode"`
}

// generateDRADevices creates the DeviceClass and one ResourceSlice per generated node
func generateDRADevices(ctx context.Context, c client.Client, config DRAConfig) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate DRA devices")
	log.Info("Start DRA device generation", "devicesPerNode", config.DevicesPerNode)
	defer log.Info("End DRA device generation")

	deviceClass := testingdra.MakeDeviceClass(controllers.DRADeviceClassName).
		CELSelector(fmt.Sprintf("device.driver == '%s'", controllers.DRADriverName)).
		Obj()
	deviceClass.Labels = map[string]string{CleanupLabel: "true"}
	if err := c.Create(ctx, deviceClass); err != nil {
		return fmt.Errorf("creating DeviceClass: %w", err)
	}

	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels{tasNodeGroupLabel: "tas"}); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return errors.New("dra requires the topology nodes to be generated first")
	}

	for _, node := range nodes.Items {
		slice := utiltesting.MakeResourceSlice(node.Name+"-gpus", controllers.DRADriverName).
			NodeName(node.Name).
			Pool(node.Name, 1, 1)
		for i := range config.DevicesPerNode {
			slice = slice.Device(fmt.Sprintf("gpu-%d", i))
		}
		obj := slice.Obj()
		obj.Labels = map[string]string{CleanupLabel: "true"}
		if err := c.Create(ctx, obj); err != nil {
			return fmt.Errorf("creating ResourceSlice for node %s: %w", node.Name, err)
		}
	}

	log.Info("Successfully generated DRA devices", "nodes", len(nodes.Items))
	return nil
}
