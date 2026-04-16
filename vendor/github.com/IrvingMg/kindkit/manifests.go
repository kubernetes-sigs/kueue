package kindkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

const (
	crdEstablishedTimeout  = 60 * time.Second
	crdEstablishedInterval = 500 * time.Millisecond
)

// ApplyManifests applies multi-document Kubernetes YAML to the cluster
// using server-side apply with field manager "kindkit". Documents are
// applied in the order they appear; documents with no kind are skipped,
// and namespaced resources without a metadata.namespace default to
// "default".
//
// When a document is a CustomResourceDefinition, ApplyManifests waits
// for Kubernetes to mark the CRD as Established before applying the
// next document, so a single call can install a CRD together with
// the custom resources that use it.
func (c *Cluster) ApplyManifests(ctx context.Context, manifests []byte) error {
	cfg, err := c.RESTConfig()
	if err != nil {
		return fmt.Errorf("get REST config: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create discovery client: %w", err)
	}

	mapper, err := newRESTMapper(disc)
	if err != nil {
		return fmt.Errorf("build REST mapper: %w", err)
	}

	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifests), 4096)
	decodingSerializer := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)

	for {
		var rawObj runtime.RawExtension
		if err := decoder.Decode(&rawObj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decode YAML document: %w", err)
		}
		if rawObj.Raw == nil {
			continue
		}

		obj := &unstructured.Unstructured{}
		if _, _, err := decodingSerializer.Decode(rawObj.Raw, nil, obj); err != nil {
			return fmt.Errorf("decode to unstructured: %w", err)
		}
		if obj.GetKind() == "" {
			continue
		}

		dr, err := resourceClient(dynClient, mapper, obj)
		if err != nil {
			return fmt.Errorf("resolve resource for %s %q: %w",
				obj.GetKind(), obj.GetName(), err)
		}

		if _, err := dr.Patch(ctx, obj.GetName(), types.ApplyPatchType, rawObj.Raw, metav1.PatchOptions{
			FieldManager: "kindkit",
		}); err != nil {
			return fmt.Errorf("apply %s %q: %w", obj.GetKind(), obj.GetName(), err)
		}

		// Wait for CRDs to be established before processing subsequent resources.
		if obj.GetKind() == "CustomResourceDefinition" {
			if err := waitForCRDEstablished(ctx, dr, obj.GetName()); err != nil {
				return fmt.Errorf("wait for CRD %q: %w", obj.GetName(), err)
			}
			mapper, err = newRESTMapper(disc)
			if err != nil {
				return fmt.Errorf("refresh REST mapper after CRD %q: %w", obj.GetName(), err)
			}
		}
	}

	return nil
}

// resourceClient resolves GVK to GVR and returns the appropriate
// dynamic client for the object's scope.
func resourceClient(dynClient dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		return dynClient.Resource(mapping.Resource).Namespace(ns), nil
	}
	return dynClient.Resource(mapping.Resource), nil
}

func newRESTMapper(disc discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	groupResources, err := restmapper.GetAPIGroupResources(disc)
	if err != nil {
		return nil, err
	}
	return restmapper.NewDiscoveryRESTMapper(groupResources), nil
}

// waitForCRDEstablished polls a CRD until its Established condition is True.
func waitForCRDEstablished(ctx context.Context, dr dynamic.ResourceInterface, name string) error {
	ctx, cancel := context.WithTimeout(ctx, crdEstablishedTimeout)
	defer cancel()

	for {
		obj, err := dr.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get CRD: %w", err)
		}

		conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
		if err == nil && found {
			for _, c := range conditions {
				cond, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if cond["type"] == "Established" && cond["status"] == string(metav1.ConditionTrue) {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for CRD to be established")
		case <-time.After(crdEstablishedInterval):
		}
	}
}
