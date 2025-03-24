# Build and Run locally

## Prerequisites
You need a kubernetes cluster running kueue.
If you don't have a running cluster, you can create one using kind and install kueue using helm.

```
kind create cluster
kind get kubeconfig > kubeconfig
export KUBECONFIG=$PWD/kubeconfig
helm install kueue oci://us-central1-docker.pkg.dev/k8s-staging-images/charts/kueue \
            --version="v0.9.1" --create-namespace --namespace=kueue-system
```

## Build
Clone the kueue repository and build the projects:

```
git clone https://github.com/kubernetes-sigs/kueue
cd kueue/cmd/experimental/kueue-viz
KUEUE_VIZ_HOME=$PWD
kubectl create ns kueue-viz
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

and bind it to the service account `default` in the `kueue-viz` namespace:

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
  namespace: kueue-viz
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





