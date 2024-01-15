#!/usr/bin/env python3

import argparse
from kubernetes import config, client

# create_job.py
# This example will demonstrate full steps to submit a Job.

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue Job Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--job-name",
        help="generateName field to set for job",
        default="sample-job-",
    )
    parser.add_argument(
        "--image",
        help="container image to use",
        default="gcr.io/k8s-staging-perf-tests/sleep:v0.1.0",
    )
    parser.add_argument(
        "--args",
        nargs="+",
        help="args for container",
        default=["30s"],
    )
    return parser


def generate_job_crd(job_name, image, args):
    """
    Generate an equivalent job CRD to sample-job.yaml
    """
    metadata = client.V1ObjectMeta(
        generate_name=job_name, labels={"kueue.x-k8s.io/queue-name": "user-queue"}
    )

    # Job container
    container = client.V1Container(
        image=image,
        name="dummy-job",
        args=args,
        resources={
            "requests": {
                "cpu": 1,
                "memory": "200Mi",
            }
        },
    )

    # Job template
    template = {"spec": {"containers": [container], "restartPolicy": "Never"}}
    return client.V1Job(
        api_version="batch/v1",
        kind="Job",
        metadata=metadata,
        spec=client.V1JobSpec(
            parallelism=1, completions=3, suspend=True, template=template
        ),
    )


def main():
    """
    Run a job.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()

    # Generate a CRD spec
    crd = generate_job_crd(args.job_name, args.image, args.args)
    batch_api = client.BatchV1Api()
    print(f"📦️ Container image selected is {args.image}...")
    print(f"⭐️ Creating sample job with prefix {args.job_name}...")
    batch_api.create_namespaced_job("default", crd)
    print(
        'Use:\n"kubectl get queue" to see queue assignment\n"kubectl get jobs" to see jobs'
    )


if __name__ == "__main__":
    main()
