# Pending Workload Pods

## Severity: Info

## Impact
Knowledge of pods in a prolonged pending state will allow users to troubleshoot and fix any issues in order to run their workloads successfully.

## Summary

This alert is triggered when a pod is in the pending state for more than 3 days.

## Steps

1. Identify the pending pod in your project namespace. Update the project namespace below to the name of your project namespace.
```bash
namespace=< project-namespace >
oc get pods -A --field-selector=status.phase=Pending # This will show all pods in the cluster with Pending status
oc get pods -n $namespace --field-selector=status.phase=Pending # This will show all pods in the specified namespace with Pending status
```

2. Get further details on the pod.
```bash
pod=< pod-name >
oc describe pod $pod -n $namespace
```

3. Review the pod logs and determine why it is in a pending state. 
```bash
oc logs $pod -n $namespace
```

4. Review the pod events in order to determine why it is in a pending state. 
```bash
oc get events --field-selector involvedObject.name=$pod --namespace=$namespace
```

5. Review the results of the steps above to determine the best course of action for successfully running the workload.
