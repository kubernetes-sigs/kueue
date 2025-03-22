# KueueViz 

KueueViz is a visualization dashboard designed to provide real-time insights into Kueue, a Kubernetes-native job queueing system. It offers an intuitive UI to monitor workload statuses, queue utilization, resource allocation, and scheduling efficiency.

![KueueViz Dashboard](../../assets/kueueviz.png)

# Installation
To install KueueViz, just follow the [installation guide](INSTALL.md) or just use the helm command:

```
KUEUE_VERSION=v0.11.0 \
helm upgrade --install kueue oci://us-central1-docker.pkg.dev/k8s-staging-images/kueue/charts/kueue \
  --version=$KUEUE_VERSION
  --namespace kueue-system \
  --set enableKueueViz=true \
  --create-namespace
```

# Contribution
If you want to contribute to `KueueViz` please follow the 
[contributing guide](CONTRIBUTING.md)


