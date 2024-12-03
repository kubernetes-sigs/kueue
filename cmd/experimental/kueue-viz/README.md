# Build and run on OpenShift


```
git clone https://github.com/akram/kueue-viz.git
oc new-project kueue-viz
KUEUE_VIZ_HOME=$PWD/kueue-viz
```

then:

## Authorize
Creating a cluster role that just has read only access on
`kueue` objects and pods, nodes and events.

```
oc create clusterrole kueue-backend-read-access --verb=get,list,watch \
          --resource=workloads,clusterqueues,localqueues,resourceflavors,pods,workloadpriorityclass,events,nodes
oc adm policy add-cluster-role-to-user kueue-backend-read-access -z default
```

## Build

```
for i in  backend frontend 
do
   oc new-build . --name $i --context-dir=$i 
done
```

## Deploy
```
oc new-app backend  --name=backend
oc new-app frontend --name=frontend
```

## Expose
```
oc create route edge --service=svc/backend
oc create route edge --service=svc/frontend
```

## Configure
```
BACKEND_URL=$(oc get route backend -o jsonpath='{.spec.host}')
FRONTEND_URL=$(oc get route frontend -o jsonpath='{.spec.host}')
oc set env deployment/backend  FRONTEND_URL=https://$FRONTEND_URL
oc set env deployment/frontend REACT_APP_BACKEND_URL=https://$BACKEND_URL \
                               REACT_APP_WEBSOCKET_URL=wss://$BACKEND_URL
```


## Test

```
oc create -f https://raw.githubusercontent.com/opendatahub-io/distributed-workloads/2c6a14f792b8d94ad3fc2146316e52ace33b6a1e/examples/kueue-usage/kueue-with-jobs/00-common.yaml
```
And check that you have some data in the Resource Flavors tab of the application.

## Improve

See [contribution guide](CONTRIBUTING.md)
