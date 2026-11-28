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
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
IMAGES = ("kueue", "importer", "kueueviz-backend", "kueueviz-frontend",
          "kueue-populator", "kueue-priority-booster")
ATTESTED = ("kueue", "kueueviz-backend", "kueueviz-frontend", "kueue-populator", "kueue-priority-booster")

CALLER_MAKE_SETTINGS = ("MAKEFLAGS", "GNUMAKEFLAGS", "PLATFORMS")


class ImageBuildTest(unittest.TestCase):
    def build_images(self, fail="", settings=(), target="image-presubmit", digest=None):
        """Run real Make recipes with a recording stub and optional failure injection.

        digest is None to leave DIGEST_DIR unset, "absolute" to pass an absolute path,
        or "relative" to pass a path relative to the repository root.
        """
        directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, directory, True)
        self.directory = directory
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
        if "--metadata-file" in args:
            # buildx writes the digest file, so a missing DIGEST_DIR fails the build.
            pathlib.Path(args[args.index("--metadata-file") + 1]).write_text('{"containerimage.digest": "sha256:test"}')
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
        inherited = {key: value for key, value in os.environ.items() if key not in CALLER_MAKE_SETTINGS}
        env = {**inherited, "IMAGE_TEST_LOG": str(log), "IMAGE_TEST_FAIL": fail,
               "IMAGE_TEST_LOCK": str(directory / "build.lock")}
        if target != "image-presubmit":
            env["BUILDX_BUILDER"] = "test-builder"
        make_settings = [*settings]
        if digest == "absolute":
            make_settings.append(f"DIGEST_DIR={directory / 'digests'}")
        elif digest == "relative":
            make_settings.append(f"DIGEST_DIR={os.path.relpath(directory / 'digests', ROOT)}")
        # The image pipeline must remain sequential even under parallel verify.
        # Execute the real parent and component Makefiles, replacing only Docker.
        result = subprocess.run(
            ["make", "--no-print-directory", "-j4", target,
             f"DOCKER_BUILDX_CMD={docker} buildx", "IMAGE_BUILD_RETRY=env",
             "HOST_IMAGE_PLATFORM=linux/amd64", "GIT_TAG=test", "GIT_COMMIT=test",
             "RELEASE_BRANCH=main", "IMAGE_REGISTRY=example.test/kueue",
             "IMAGE_BUILD_EXTRA_OPTS=--progress=plain", *make_settings],
            cwd=ROOT, env=env, text=True, capture_output=True,
        )
        return result, [json.loads(line) for line in log.read_text().splitlines()]

    def builds_by_image(self, commands):
        """Key each buildx build by the image it tags, such as example.test/kueue/kueue:test."""
        builds = {}
        for command in commands:
            args = command["args"]
            if args[:2] != ["buildx", "build"]:
                continue
            names = [name for name in IMAGES if f"example.test/kueue/{name}:test" in args]
            self.assertEqual(len(names), 1, args)
            builds[names[0]] = args
        self.assertEqual(set(builds), set(IMAGES))
        return builds

    def metadata_files(self, args):
        return [args[index + 1] for index, arg in enumerate(args) if arg == "--metadata-file"]

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

    def test_attested_images_record_their_digest_when_digest_dir_is_set(self):
        """Ask buildx for a metadata file only for images that staging attests."""
        result, commands = self.build_images(digest="absolute")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        builds = self.builds_by_image(commands)
        digests = os.path.realpath(self.directory / "digests")
        for name in ATTESTED:
            with self.subTest(image=name):
                metadata = self.metadata_files(builds[name])
                self.assertEqual(len(metadata), 1)
                self.assertEqual(os.path.realpath(metadata[0]), os.path.join(digests, f"{name}.json"))
        self.assertEqual(self.metadata_files(builds["importer"]), [])
        self.assertTrue((self.directory / "digests").is_dir())

    def test_relative_digest_dir_resolves_to_an_absolute_path(self):
        """Resolve DIGEST_DIR at the top level, because sub-make recipes run in other directories."""
        result, commands = self.build_images(digest="relative")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        builds = self.builds_by_image(commands)
        digests = os.path.realpath(self.directory / "digests")
        for name in ATTESTED:
            with self.subTest(image=name):
                metadata = self.metadata_files(builds[name])
                self.assertEqual(len(metadata), 1)
                self.assertTrue(os.path.isabs(metadata[0]), metadata[0])
                self.assertEqual(os.path.realpath(metadata[0]), os.path.join(digests, f"{name}.json"))

    def test_digest_metadata_and_directory_are_absent_without_digest_dir(self):
        """Keep build commands unchanged and create no digest directory when DIGEST_DIR is unset."""
        result, commands = self.build_images()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for command in commands[1:-1]:
            self.assertNotIn("--metadata-file", command["args"])
        self.assertFalse((self.directory / "digests").exists())

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

    def test_e2e_image_budget_is_scoped_and_can_be_overridden(self):
        cases = (
            ("", (), "", ""),
            ("test-e2e-kueueviz", (), "1", "4"),
            ("test-multikueue-e2e-baseline", (), "1", "4"),
            ("test-multikueue-e2e-extended", (), "", ""),
            ("test-e2e-kueueviz", ("IMAGE_BUILD_GOMAXPROCS=3", "IMAGE_BUILD_GO_PARALLELISM=2"), "3", "2"),
            ("test-multikueue-e2e-baseline", ("IMAGE_BUILD_GOMAXPROCS=3", "IMAGE_BUILD_GO_PARALLELISM=2"), "3", "2"),
            ("test-e2e-kueueviz", ("IMAGE_BUILD_GOMAXPROCS=", "IMAGE_BUILD_GO_PARALLELISM="), "", ""),
            ("test-multikueue-e2e-baseline", ("IMAGE_BUILD_GOMAXPROCS=", "IMAGE_BUILD_GO_PARALLELISM="), "", ""),
        )
        for suite, overrides, gomaxprocs, parallelism in cases:
            with self.subTest(suite=suite, overrides=overrides):
                # Exercise the real combined Make invocation without downloading
                # kind or running a cluster; only the image recipe should execute.
                settings = ("-o", "kind", *overrides)
                if suite:
                    settings += ("-o", suite, suite)
                result, commands = self.build_images(target="kind-image-build", settings=settings)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(len(commands), 1)
                args = commands[0]["args"]
                self.assertIn("--platform=linux/amd64", args)
                self.assertIn("--load", args)
                self.assertIn(f"IMAGE_BUILD_GOMAXPROCS={gomaxprocs}", args)
                self.assertIn(f"IMAGE_BUILD_GO_PARALLELISM={parallelism}", args)

    def test_failed_build_stops_remaining_images_and_removes_builder(self):
        result, commands = self.build_images("build")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(commands), 4)
        self.assertEqual(commands[-1]["args"], ["buildx", "rm", "test-builder"])

    def test_caller_make_flags_and_platforms_do_not_change_the_builds(self):
        caller = {"MAKEFLAGS": "k -- PLATFORMS=linux/arm64", "GNUMAKEFLAGS": "k", "PLATFORMS": "linux/arm64"}
        with mock.patch.dict(os.environ, caller):
            result, commands = self.build_images("build")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(commands), 4)
        self.assertIn("--platform=linux/amd64,linux/arm64,linux/s390x,linux/ppc64le", commands[1]["args"])
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
