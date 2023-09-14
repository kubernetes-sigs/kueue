# Pod Taints & Tolerations Controller

This controller adds experimental support for bare Pod workloads, targeting clusters that predate support for [scheduling gates](https://kubernetes.io/blog/2022/12/26/pod-scheduling-readiness-alpha/) (alpha in Kubernetes 1.26). This controller acts as a translation layer between the Pod API and the Kueue Workload API. It implements queue admission by updating Pod tolerations.

NOTE: Node taints should be pre-configured by cluster administrators.

This directory should be considered a reference implementation of how Kueue can be extended out-of-tree to add support for different workload types.

## Local Quickstart

### Setup

Create a kind cluster with Kueue installed.

```bash
./hack/kind-cluster.sh
```

Configure Kueue.

```bash
kubectl apply -f examples/queue.yaml
kubectl apply -f examples/priorities.yaml
```

In another terminal, start the controller.

```bash
go run . --admission-taint-key=company.com/kueue-admission
````

### Toleration-based Admission and ResourceFlavors

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

## Deployment

Configure an image.

```bash
export IMAGE=your-registry.com/kueue-podtaintstolerations
export TAG=v1.0.0
```

Build and push the image.

```bash
docker buildx build . -t ${IMAGE}:${TAG}
docker push ${IMAGE}:${TAG}
```

Create a directory.

```bash
mkdir kueue-podtaintstolerations
cat << EOF > kueue-podtaintstolerations/kustomization.yaml
resources:
- "https://github.com/kubernetes-sigs/kueue//cmd/experimental/podtaintstolerations/config?ref=main"
images:
- name: controller
  newName: ${IMAGE}
  newTag: ${TAG}
EOF
```

Apply into our cluster.

```bash
kubectl apply -k ./kueue-podtaintstolerations
```

