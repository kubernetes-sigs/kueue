# Simple installation

KueueViz can be installed using `kubectl` with the following command:

```
kubectl create -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.13.2/kueueviz.yaml
```
If you are using `kind` and that you don't have an `ingress` controller, you can use `port-forward` to 
configure and run `KueueViz`:

```
kubectl -n kueue-system port-forward svc/kueue-kueueviz-backend 8080:8080 &
kubectl -n kueue-system set env deployment kueue-kueueviz-frontend REACT_APP_WEBSOCKET_URL=ws://localhost:8080
kubectl -n kueue-system port-forward svc/kueue-kueueviz-frontend 3000:8080
```

`KueueViz` will the be reachable on your browser at: http://localhost:3000

# Installation with helm

KueueViz can be installed using helm using the following command and 
by ensuring that `enableKueueViz` is set to `true`:

```
helm upgrade --install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version="0.13.2"
  --namespace kueue-system \
  --set enableKueueViz=true \
  --create-namespace
```


# Build and Run locally

If you want to run KueueViz locally for development or debugging purposes, you need to go
through the following steps.

## Prerequisites
You need a kubernetes cluster running kueue.
If you don't have a running cluster, you can create one using kind and install kueue using helm.

```
kind create cluster
kind get kubeconfig > kubeconfig
export KUBECONFIG=$PWD/kubeconfig
helm install kueue oci://us-central1-docker.pkg.dev/k8s-staging-images/charts/kueue \
            --version="0.13.2" --create-namespace --namespace=kueue-system
```

## Build
Clone the kueue repository and build the projects:

```
git clone https://github.com/kubernetes-sigs/kueue
cd kueue/cmd/kueueviz
KUEUE_VIZ_HOME=$PWD
kubectl create ns kueueviz
cd backend && make && cd ..
cd frontend && make && cd ..
```

## Authorize
Create a cluster role that just has read only access on
`kueue` objects and pods, nodes and events.

Create the cluster role:

```
kubectl create clusterrole kueue-backend-read-access --verb=get,list,watch \
         --resource=workloads,clusterqueues,localqueues,resourceflavors,pods,workloadpriorityclass,events,nodes
```

and bind it to the service account `default` in the `kueueviz` namespace:

```
kubectl create -f - << EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kueue-backend-read-access-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kueue-backend-read-access
subjects:
- kind: ServiceAccount
  name: default
  namespace: kueueviz
EOF
```
## Run

`backend` uses `CompilerDaemon` to automatically rebuild go code.
You may need to install `CompilerDaemon` with the following command:

```
go get github.com/githubnemo/CompileDaemon
```

Then, in a first terminal run:

```
cd backend && make debug
```

And, in another terminal run:

```
cd frontend && make debug
```

## Test
Create test data using the resources in `examples/` directory.

```
kubectl create -f examples/
```
And check that you have some data on the dashboard.

## Improve
See [contribution guide](CONTRIBUTING.md)



