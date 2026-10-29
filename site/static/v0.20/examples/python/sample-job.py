#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.9"
# dependencies = [
#     "kubernetes",
# ]
# ///

import argparse
from kubernetes import config, client

# create_job.py
# This example will demonstrate full steps to submit a Job.

# Make sure your cluster is running!


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
        default="registry.k8s.io/e2e-test-images/agnhost:2.53",
    )
    parser.add_argument(
        "--args",
        nargs="+",
        help="args for container",
        default=["entrypoint-tester", "hello", "world"],
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
    config.load_kube_config()
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
