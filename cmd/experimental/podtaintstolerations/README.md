# Pod Taints & Tolerations Controller

This controller adds experimental support for bare Pod workloads, targeting clusters that predate support for [scheduling gates](https://kubernetes.io/blog/2022/12/26/pod-scheduling-readiness-alpha/) (alpha in Kubernetes 1.26). This controller acts as a translation layer between the Pod API and the Kueue Workload API. It implements queue admission by updating Pod tolerations.

NOTE: Node taints need to be pre-configured by cluster administrators.

This directory should be considered a reference implementation of how Kueue can be extended out-of-tree to add support for different workload types.

## Local Quickstart

### Cluster Setup

Create a kind cluster and taint the Nodes.

```bash
kind create cluster --config ./hack/kind-cluster.yaml

kubectl taint nodes kind-worker2 tier=spot:NoSchedule
kubectl taint nodes kind-worker2 kueue.x-k8s.io/kueue-admission:NoSchedule

kubectl taint nodes kind-worker3 tier=regular:NoSchedule
kubectl taint nodes kind-worker3 kueue.x-k8s.io/kueue-admission:NoSchedule
```

Install Kueue.

```bash
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.4.1/manifests.yaml

# Wait for readiness.
kubectl rollout status deployment -n kueue-system kueue-controller-manager
```

Configure an image.

```bash
export IMAGE=your-registry.com/kueue-podtaintstolerations
export TAG=v1.0.0
```

Build the controller image.

```bash
docker buildx build . -t ${IMAGE}:${TAG}

# Push the image if you are deploying to a remote cluster.
# docker push ${IMAGE}:${TAG}

# Load the image into the local kind cluster.
kind load docker-image $IMAGE:$TAG
```

Create a directory for Kustomize.

```bash
mkdir manifests
cat << EOF > manifests/kustomization.yaml
resources:
- ../config
# Remote alternative:
# - "https://github.com/kubernetes-sigs/kueue//cmd/experimental/podtaintstolerations/config?ref=main"
images:
- name: controller
  newName: ${IMAGE}
  newTag: ${TAG}
EOF
```

Apply the Pods-Taints-Tolerations controller into the cluster.

```bash
kubectl apply -k ./manifests
```

### Toleration-based Admission and ResourceFlavors

Configure Kueue.

```bash
kubectl apply -f examples/queue.yaml
kubectl apply -f examples/priorities.yaml
```

Create a standard-priority Pod.

```bash
kubectl create -f ./examples/pod.yaml
```

Verify a Kueue Workload object was created for the Pod.

```bash
kubectl get workloads
```

Because the only ResourceFlavor available to the configured ClusterQueue targets "spot" Nodes, this Pod should be scheduled on the matching Node (`kind-worker2`).

```bash
kubectl get pods -o wide
```

Note the taints that were set on this Node by the cluster creation script.

```bash
kubectl get nodes kind-worker2 -o jsonpath="{.spec.taints}"
```

Note the tolerations that were set to admit and direct the Pod to the correct "spot" Node.

```bash
kubectl get pods <name-of-pod> -o jsonpath="{.spec.tolerations}"
```

### Preemption

The ClusterQueue in this example is setup to preempt workloads of lower priority. To test this, create a Pod with a higher priority.

```bash
kubectl create -f ./examples/pod-highpriority.yaml
```

Note that preemption is implemented via deletion of the original Pod. There should only be a single "high-priority" Pod running now.

```bash
kubectl get pods
```

### Cleanup

Delete the local cluster once you are done.

```bash
kind delete cluster
```

## Testing

```bash
make test-e2e
```
