# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Exercise image-presubmit's real Make recipes without a Docker daemon."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ImageBuildTest(unittest.TestCase):
    def build_images(self, fail="", settings=(), target="image-presubmit"):
        """Run real Make recipes with a recording stub and optional failure injection."""
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            docker = directory / "docker"
            docker.write_text('''#!/usr/bin/env python3
import fcntl, json, os, pathlib, sys, time
args = sys.argv[1:]
with pathlib.Path(os.environ["IMAGE_TEST_LOG"]).open("a") as log:
    log.write(json.dumps({"args": args, "builder": os.environ.get("BUILDX_BUILDER")}) + "\\n")
if args[:2] == ["buildx", "create"]:
    if os.environ["IMAGE_TEST_FAIL"] == "create":
        sys.exit(1)
    print("test-builder")
elif args[:2] == ["buildx", "build"]:
    assert os.environ["BUILDX_BUILDER"] == "test-builder"
    with pathlib.Path(os.environ["IMAGE_TEST_LOCK"]).open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            sys.exit("Overlapping image build")
        # Keep simulated builds active long enough to detect overlapping recipes.
        time.sleep(0.2)
        if os.environ["IMAGE_TEST_FAIL"] == "build" and "./cmd/importer/Dockerfile" in args:
            sys.exit(1)
elif args == ["buildx", "rm", "test-builder"]:
    if os.environ["IMAGE_TEST_FAIL"] == "cleanup":
        sys.exit(1)
else:
    sys.exit("Unexpected Docker command: " + repr(args))
''')
            docker.chmod(0o755)
            log = directory / "commands.jsonl"
            env = {**os.environ, "IMAGE_TEST_LOG": str(log), "IMAGE_TEST_FAIL": fail,
                   "IMAGE_TEST_LOCK": str(directory / "build.lock")}
            if target == "image-build":
                env["BUILDX_BUILDER"] = "test-builder"
            # The image pipeline must remain sequential even under parallel verify.
            # Execute the real parent and component Makefiles, replacing only Docker.
            result = subprocess.run(
                ["make", "--no-print-directory", "-j4", target,
                 f"DOCKER_BUILDX_CMD={docker} buildx", "IMAGE_BUILD_RETRY=env",
                 "HOST_IMAGE_PLATFORM=linux/amd64", "GIT_TAG=test", "GIT_COMMIT=test",
                 "RELEASE_BRANCH=main", "IMAGE_REGISTRY=example.test/kueue",
                 "IMAGE_BUILD_EXTRA_OPTS=--progress=plain", *settings],
                cwd=ROOT, env=env, text=True, capture_output=True,
            )
            return result, [json.loads(line) for line in log.read_text().splitlines()]

    def test_all_images_share_one_builder_and_keep_platform_coverage(self):
        """Keep one builder and apply parallelism settings only to the manager."""
        result, commands = self.build_images()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(commands[0]["args"], ["buildx", "create", "--driver", "docker-container"])
        self.assertEqual(commands[-1]["args"], ["buildx", "rm", "test-builder"])
        builds = commands[1:-1]
        self.assertEqual(len(builds), 6)
        dockerfiles = ["Dockerfile", "./cmd/importer/Dockerfile", "./cmd/kueueviz/backend/Dockerfile",
                       "./cmd/kueueviz/frontend/Dockerfile", "Dockerfile", "Dockerfile"]
        images = ["kueue", "importer", "kueueviz-backend", "kueueviz-frontend",
                  "kueue-populator", "kueue-priority-booster"]
        for index, (command, dockerfile) in enumerate(zip(builds, dockerfiles)):
            with self.subTest(image=index):
                self.assertEqual(command["builder"], "test-builder")
                args = command["args"]
                self.assertEqual(args[:2], ["buildx", "build"])
                self.assertIn(f"example.test/kueue/{images[index]}:test", args)
                self.assertIn("--progress=plain", args)
                expected_platforms = "linux/amd64,linux/arm64,linux/s390x,linux/ppc64le" if index == 0 else "linux/amd64"
                self.assertIn(f"--platform={expected_platforms}", args)
                if index:
                    self.assertEqual(args[args.index("-f") + 1], dockerfile)
                    self.assertIn("--load", args)
                else:
                    self.assertNotIn("--load", args)
                    self.assertIn("IMAGE_BUILD_GOMAXPROCS=2", args)
                    self.assertIn("IMAGE_BUILD_GO_PARALLELISM=2", args)
                self.assertNotIn("--push", args)
                if index:
                    self.assertFalse(any(arg.startswith("IMAGE_BUILD_GOMAXPROCS=") or
                                         arg.startswith("IMAGE_BUILD_GO_PARALLELISM=") for arg in args))

    def test_manager_parallelism_can_be_overridden_or_left_automatic(self):
        """Route independent budgets and explicit automatic defaults to the manager."""
        for gomaxprocs, parallelism in (("1", "4"), ("", "")):
            with self.subTest(gomaxprocs=gomaxprocs, parallelism=parallelism):
                result, commands = self.build_images(settings=(
                    f"IMAGE_PRESUBMIT_GOMAXPROCS={gomaxprocs}", f"IMAGE_PRESUBMIT_GO_PARALLELISM={parallelism}",
                ))
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn(f"IMAGE_BUILD_GOMAXPROCS={gomaxprocs}", commands[1]["args"])
                self.assertIn(f"IMAGE_BUILD_GO_PARALLELISM={parallelism}", commands[1]["args"])

    def test_standalone_image_build_keeps_automatic_parallelism(self):
        result, commands = self.build_images(target="image-build")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(commands), 1)
        self.assertEqual(commands[0]["args"][:2], ["buildx", "build"])
        self.assertIn("IMAGE_BUILD_GOMAXPROCS=", commands[0]["args"])
        self.assertIn("IMAGE_BUILD_GO_PARALLELISM=", commands[0]["args"])

    def test_failed_build_stops_remaining_images_and_removes_builder(self):
        result, commands = self.build_images("build")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(commands), 4)
        self.assertEqual(commands[-1]["args"], ["buildx", "rm", "test-builder"])

    def test_failed_creation_does_not_build_or_remove_another_builder(self):
        result, commands = self.build_images("create")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(commands), 1)

    def test_cleanup_failure_is_reported(self):
        result, commands = self.build_images("cleanup")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(commands[-1]["args"], ["buildx", "rm", "test-builder"])


if __name__ == "__main__":
    unittest.main()
