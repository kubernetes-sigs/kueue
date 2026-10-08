#!/usr/bin/env python3

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

"""Exercise the action with a local Git remote and simulated GitHub responses.

Requires git, bash, jq and the dependencies in requirements.txt.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml


class CreateReleaseTagTest(unittest.TestCase):
    version = "v0.0.1"

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.work = self.root / "work"
        self.origin = self.root / "origin.git"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.real_git = shutil.which("git")
        self.env = {
            **os.environ,
            "GIT_CONFIG_GLOBAL": str(self.root / "gitconfig"),
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_TERMINAL_PROMPT": "0",
            "VERSION": self.version,
            "CHANGELOG": "Release notes\n\n- A fix",
            "TARGET_REPO": "owner/repo",
            "GITHUB_OUTPUT": str(self.root / "output"),
            "GH_RESPONSE": json.dumps({"data": {"repository": {"release": {"isDraft": True}}}}),
            "REAL_GIT": self.real_git,
            "TEST_ORIGIN": str(self.origin),
            "CONCURRENT_MARKER": str(self.root / "concurrent-update"),
        }
        self.git("config", "--global", "user.name", "Release test")
        self.git("config", "--global", "user.email", "release-test@example.com")
        self.git("config", "--global", "tag.gpgSign", "false")
        self.git("init", "--bare", str(self.origin))
        self.git("init", str(self.work))
        self.git("remote", "add", "origin", str(self.origin), cwd=self.work)
        self.git("-c", "commit.gpgSign=false", "commit", "--allow-empty", "-m", "Old commit", cwd=self.work)
        self.git("tag", "-a", self.version, "-m", "Old tag", cwd=self.work)
        self.git("push", "origin", f"refs/tags/{self.version}", cwd=self.work)
        self.old_tag = self.remote_tag()
        self.git("-c", "commit.gpgSign=false", "commit", "--allow-empty", "-m", "Release commit", cwd=self.work)
        self.head = self.git("rev-parse", "HEAD", cwd=self.work).stdout.strip()

        self.write_executable(self.bin / "gh", '''
import json
import os
import subprocess
import sys

args = sys.argv[1:]
if args == ["auth", "setup-git"]:
    sys.exit(0)
if os.environ.get("GH_ERROR"):
    print(os.environ["GH_ERROR"], file=sys.stderr)
    sys.exit(1)
response = os.environ["GH_RESPONSE"]
# Support the original action too, so these tests can demonstrate the regression.
if args[:2] == ["release", "view"]:
    release = json.loads(response)["data"]["repository"]["release"]
    if release is None:
        print("release not found", file=sys.stderr)
        sys.exit(1)
    response = json.dumps(release)
elif args[:2] != ["api", "graphql"]:
    raise AssertionError(args)
query = args[args.index("--jq") + 1]
sys.exit(subprocess.run(["jq", "-r", query], input=response, text=True).returncode)
''')
        self.write_executable(self.bin / "git", '''
import os
import sys
import subprocess
from pathlib import Path

args = sys.argv[1:]
real_git = os.environ["REAL_GIT"]
if args[0] == "ls-remote" and os.environ.get("FAIL_LOOKUP"):
    print("injected remote lookup failure", file=sys.stderr)
    sys.exit(1)
if args[0] == "tag" and "-a" in args and os.environ.get("FAIL_TAG"):
    print("injected tag creation failure", file=sys.stderr)
    sys.exit(1)
marker = Path(os.environ["CONCURRENT_MARKER"])
if args[0] == "push" and os.environ.get("CONCURRENT_TAG") and not marker.exists():
    subprocess.run([real_git, "-C", os.environ["TEST_ORIGIN"], "update-ref",
                    "refs/tags/" + os.environ["VERSION"], os.environ["CONCURRENT_TAG"]], check=True)
    marker.touch()
os.execv(real_git, [real_git, *args])
''')
        self.env["PATH"] = str(self.bin) + os.pathsep + os.environ["PATH"]
        action = Path(__file__).resolve().parents[2] / ".github/actions/create-release-tag/action.yml"
        self.script = self.root / "action.sh"
        self.script.write_text(yaml.safe_load(action.read_text())["runs"]["steps"][0]["run"])

    def write_executable(self, path, body):
        path.write_text(f"#!{sys.executable}\n" + body)
        path.chmod(0o755)

    def git(self, *args, cwd=None):
        return subprocess.run([self.real_git, *args], cwd=cwd or self.root,
                              env=self.env, capture_output=True, text=True, check=True)

    def remote_tag(self):
        refs = self.git("for-each-ref", "--format=%(objectname)",
                        f"refs/tags/{self.version}", cwd=self.origin)
        return refs.stdout.strip()

    def run_action(self, **env):
        Path(self.env["GITHUB_OUTPUT"]).write_text("")
        result = subprocess.run(["bash", "-e", "-o", "pipefail", str(self.script)],
                                cwd=self.work, env={**self.env, **env},
                                capture_output=True, text=True, check=False)
        self.output = Path(self.env["GITHUB_OUTPUT"]).read_text()
        return result

    def assert_failure(self, result):
        self.assertNotEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn("message=", self.output)
        self.assertNotIn("outcome=", self.output)

    def assert_success(self, result, outcome):
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn(f"outcome={outcome}\n", self.output)
        self.assertIn("message=", self.output)
        self.assertEqual("tag", self.git("cat-file", "-t", self.remote_tag(), cwd=self.origin).stdout.strip())
        self.assertEqual(self.head, self.git("rev-parse", f"{self.version}^{{}}", cwd=self.origin).stdout.strip())
        annotation = self.git("for-each-ref", "--format=%(contents)",
                              f"refs/tags/{self.version}", cwd=self.origin).stdout.strip()
        self.assertEqual(self.version + "\n\n" + self.env["CHANGELOG"], annotation)

    def test_lookup_errors_leave_local_and_remote_tags_unchanged(self):
        for error in ("HTTP 401: Bad credentials", "HTTP 403: Forbidden",
                      "HTTP 500: Internal Server Error", "connection timed out",
                      "GraphQL: Resource not accessible by integration"):
            with self.subTest(error=error):
                result = self.run_action(GH_ERROR=error)
                self.assert_failure(result)
                self.assertIn(error, result.stderr)
                self.assertEqual(self.old_tag, self.remote_tag())
                self.assertEqual(self.old_tag, self.git("rev-parse", self.version, cwd=self.work).stdout.strip())

    def test_invalid_response_stops_before_modifying_tags(self):
        for response in ({"data": {"repository": None}},
                         {"data": {"repository": {"release": {"isDraft": None}}}}):
            with self.subTest(response=response):
                result = self.run_action(GH_RESPONSE=json.dumps(response))
                self.assert_failure(result)
                self.assertEqual(self.old_tag, self.remote_tag())

    def test_published_release_is_not_modified(self):
        result = self.run_action(GH_RESPONSE=json.dumps({"data": {"repository": {"release": {"isDraft": False}}}}))
        self.assert_failure(result)
        self.assertIn("already published", self.output)
        self.assertEqual(self.old_tag, self.remote_tag())

    def test_empty_changelog_is_rejected(self):
        self.assert_failure(self.run_action(CHANGELOG=""))
        self.assertEqual(self.old_tag, self.remote_tag())

    def test_remote_lookup_failure_preserves_tags(self):
        self.assert_failure(self.run_action(FAIL_LOOKUP="1"))
        self.assertEqual(self.old_tag, self.remote_tag())
        self.assertEqual(self.old_tag, self.git("rev-parse", self.version, cwd=self.work).stdout.strip())

    def test_local_creation_failure_preserves_remote_tag(self):
        self.assert_failure(self.run_action(FAIL_TAG="1"))
        self.assertEqual(self.old_tag, self.remote_tag())

    def test_rejected_push_preserves_remote_tag(self):
        # Allow deletes but reject a replacement, reproducing the old delete/push gap.
        hook = self.origin / "hooks/pre-receive"
        hook.write_text('#!/bin/sh\nread -r old new ref\n[ "$new" = "0000000000000000000000000000000000000000" ]\n')
        hook.chmod(0o755)
        self.assert_failure(self.run_action())
        self.assertEqual(self.old_tag, self.remote_tag())

    def prepare_concurrent_tag(self):
        self.git("tag", "-a", "concurrent", "-m", "Another writer", cwd=self.work)
        self.git("push", "origin", "refs/tags/concurrent", cwd=self.work)
        return self.git("rev-parse", "concurrent", cwd=self.work).stdout.strip()

    def test_concurrent_update_is_not_overwritten(self):
        concurrent = self.prepare_concurrent_tag()
        self.assert_failure(self.run_action(CONCURRENT_TAG=concurrent))
        self.assertEqual(concurrent, self.remote_tag())

    def test_concurrent_creation_is_not_overwritten(self):
        concurrent = self.prepare_concurrent_tag()
        self.git("update-ref", "-d", f"refs/tags/{self.version}", cwd=self.origin)
        self.git("tag", "-d", self.version, cwd=self.work)
        self.assert_failure(self.run_action(CONCURRENT_TAG=concurrent))
        self.assertEqual(concurrent, self.remote_tag())

    def test_draft_tag_is_updated(self):
        self.assert_success(self.run_action(), "updated")

    def test_remote_tag_is_updated_without_local_tag(self):
        self.git("tag", "-d", self.version, cwd=self.work)
        self.assert_success(self.run_action(), "updated")

    def test_missing_release_can_create_tag_despite_stale_local_tag(self):
        self.git("update-ref", "-d", f"refs/tags/{self.version}", cwd=self.origin)
        result = self.run_action(GH_RESPONSE=json.dumps({"data": {"repository": {"release": None}}}))
        self.assert_success(result, "created")


if __name__ == "__main__":
    unittest.main()
