# Kueue Pod Down

## Severity: Critical

## Impact

Any workloads running on the cluster will not be able to use the Kueue component.

## Summary

This alert is triggered when the `kube_pod_status_ready` query shows that the Kueue controller pod is not ready.

## Steps

1. Check to see if the `kueue-controller` pod is running in the `redhat-ods-applications` namespace:

```bash
$ oc -n redhat-ods-applications get pods -l app.kubernetes.io/name=kueue
```

2. If the pod is not running, look at the pod's logs/events to see what may be causing the issues. Please make sure to grab the logs/events so they can be shared with the engineering team later:

```bash
# Check pod logs 
$ oc -n redhat-ods-applications logs -l app.kubernetes.io/name=kueue --prefix=true

# Check events 
$ oc -n redhat-ods-applications get events | grep pod/kueue-controller

# Check pod status fields
$ oc -n redhat-ods-applications get pods -l app.kubernetes.io/name=kueue -o jsonpath="{range .items[*]}{.status}{\"\n\n\"}{end}"
```

3. Redeploy Kueue Operator by restarting the deployment:

```bash
$ oc -n redhat-ods-applications rollout restart deployments/kueue-controller-manager
```

This should result in a new pod getting deployed, attempt step (1) again and see if the pod achieves running state.

4. If the problem persists, capture the logs and escalate to the RHOAI engineering team.
