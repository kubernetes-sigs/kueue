#!/bin/bash

# This script creates Jobs in the default namespace. 
# It assumes that the AdmissionCheckController is running in the acc-3211-system namespace in a deployment called acc-3211-controller-manager

JOB_FILE="sample-job-limits-gang.yaml"
OUTPUT_FILE="gang_test.txt"

echo "---- General Node Capacity ----" > $OUTPUT_FILE
    
# Capture node resource usage in a concise format
kubectl get nodes -o custom-columns="NAME:.metadata.name,CPU_ALLOCATABLE:.status.allocatable.cpu,MEM_ALLOCATABLE:.status.allocatable.memory,CPU_CAPACITY:.status.capacity.cpu,MEM_CAPACITY:.status.capacity.memory" >> $OUTPUT_FILE


# Run the loop to create the job and capture node resource usage
for i in {1..5}
do
    echo "Creating job (iteration $i)..."
    kubectl create -f $JOB_FILE
    
    # Wait for 16 seconds before the next iteration since a new resource snapshot is taken every 15 seconds
    sleep 16
done

{
    echo "---- Workloads state at the end ----"
    kubectl get workloads

    # Save logs of controller for the last 2 minutes (the previous cycle should last 80 seconds).
    echo "---- Controller logs ----"
    kubectl logs -n acc-3211-system deployment/acc-3211-controller-manager --since 2m
}>>$OUTPUT_FILE

echo "Test completed. Results saved to $OUTPUT_FILE."
echo "Expected behaviour: the last workloads are not admitted since the previous ones are still running and there si scarcity of resources across nodes."
echo "However after around 60 seconds the controller will try to admit them again and will find enough resources on the snapshot, since the previous jobs finished."
