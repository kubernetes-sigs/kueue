#!/usr/bin/env python3

import argparse
from kubernetes import config, client
import mpijob.models as models

# sample-mpijob.py
# This example will demonstrate full steps to submit a Job via the MPI Operator

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue MPI Operator Job Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--job-name",
        help="generateName field to set for job (job prefix does not work here)",
        default="pi",
    )
    parser.add_argument(
        "--image",
        help="container image to use",
        default="mpioperator/mpi-pi:openmpi",
    )
    parser.add_argument(
        "--command",
        help="command to run",
        default="mpirun",
    )
    parser.add_argument(
        "--args",
        nargs="+",
        help="args for container",
        default=["-n", "2", "/home/mpiuser/pi"],
    )
    return parser


def generate_job_crd(job_name, image, command, args):
    """
    Generate an equivalent job CRD to sample-job.yaml
    """
    metadata = client.V1ObjectMeta(
        name=job_name, labels={"kueue.x-k8s.io/queue-name": "user-queue"}
    )

    # containers for launcher and worker
    launcher_container = client.V1Container(
        image=image,
        name="mpi-launcher",
        command=[command],
        args=args,
        security_context=client.V1SecurityContext(run_as_user=1000),
        resources={
            "limits": {
                "cpu": 1,
                "memory": "1Gi",
            }
        },
    )

    worker_container = client.V1Container(
        image=image,
        name="mpi-worker",
        command=["/usr/sbin/sshd"],
        args=["-De", "-f", "/home/mpiuser/.sshd_config"],
        security_context=client.V1SecurityContext(run_as_user=1000),
        resources={
            "limits": {
                "cpu": 1,
                "memory": "1Gi",
            }
        },
    )

    # Create the Launcher and worker replica specs
    launcher = models.V2beta1ReplicaSpec(
        replicas=1,
        template=client.V1PodTemplateSpec(
            spec=client.V1PodSpec(containers=[launcher_container])
        ),
    )

    worker = models.V2beta1ReplicaSpec(
        replicas=2,
        template=client.V1PodTemplateSpec(
            spec=client.V1PodSpec(containers=[worker_container])
        ),
    )

    # runPolicy for jobspec
    policy = models.V2beta1RunPolicy(
        clean_pod_policy="Running", ttl_seconds_after_finished=60
    )

    # Create the jobspec
    jobspec = models.V2beta1MPIJobSpec(
        slots_per_worker=1,
        run_policy=policy,
        ssh_auth_mount_path="/home/mpiuser/.ssh",
        mpi_replica_specs={"Launcher": launcher, "Worker": worker},
    )
    return models.V2beta1MPIJob(
        metadata=metadata,
        api_version="kubeflow.org/v2beta1",
        kind="MPIJob",
        spec=jobspec,
    )


def main():
    """
    Run an MPIJob. This requires the MPI Operator to be installed.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()

    # Generate a CRD spec
    crd = generate_job_crd(args.job_name, args.image, args.command, args.args)
    crd_api = client.CustomObjectsApi()

    print(f"📦️ Container image selected is {args.image}...")
    print(f"⭐️ Creating sample job with prefix {args.job_name}...")
    crd_api.create_namespaced_custom_object(
        group="kubeflow.org",
        version="v2beta1",
        namespace="default",
        plural="mpijobs",
        body=crd,
    )
    print(
        'Use:\n"kubectl get queue" to see queue assignment\n"kubectl get jobs" to see jobs'
    )


if __name__ == "__main__":
    main()
