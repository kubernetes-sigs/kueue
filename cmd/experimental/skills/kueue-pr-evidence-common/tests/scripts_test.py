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

"""Offline tests for scripts/merge-base.sh and scripts/kueue-worktree.sh.

Each test builds a throwaway git repository; nothing is fetched.
"""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent.parent / "scripts"
MERGE_BASE = SCRIPTS / "merge-base.sh"
KUEUE_WORKTREE = SCRIPTS / "kueue-worktree.sh"


def git(repo, *args):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}
    return subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True,
                          text=True, env=env).stdout.strip()


def commit(repo, name):
    (Path(repo) / name).write_text(name, encoding="utf-8")
    git(repo, "add", name)
    git(repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", name)
    return git(repo, "rev-parse", "HEAD")


class ScriptsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.repo = self.dir / "repo"
        self.repo.mkdir()
        git(self.repo, "init", "-q", "-b", "main")
        self.base = commit(self.repo, "a")
        git(self.repo, "remote", "add", "upstream", "https://github.com/kubernetes-sigs/kueue.git")
        git(self.repo, "update-ref", "refs/remotes/upstream/main", self.base)
        git(self.repo, "checkout", "-q", "-b", "feature")
        self.head = commit(self.repo, "b")
        self.env = {**os.environ, "PR_EVIDENCE_WORKTREE_ROOT": str(self.dir / "wt")}
        self.env.pop("PR_EVIDENCE_UPSTREAM_REMOTE", None)

    def tearDown(self):
        self.tmp.cleanup()

    def run_script(self, *args, check=True):
        return subprocess.run([str(a) for a in args], capture_output=True, text=True,
                              env=self.env, check=check)

    def test_merge_base_prints_shas(self):
        out = self.run_script(MERGE_BASE, "--worktree", self.repo, "--no-fetch").stdout
        kv = dict(line.split("=", 1) for line in out.splitlines())
        self.assertEqual(kv["UPSTREAM_REMOTE"], "upstream")
        self.assertEqual(kv["UPSTREAM_REF"], "refs/remotes/upstream/main")
        self.assertEqual(kv["MERGE_BASE"], self.base)
        self.assertEqual(kv["HEAD_SHA"], self.head)

    def test_merge_base_needs_kueue_remote(self):
        git(self.repo, "remote", "set-url", "upstream", "https://github.com/example/other.git")
        res = self.run_script(MERGE_BASE, "--worktree", self.repo, "--no-fetch", check=False)
        self.assertEqual(res.returncode, 2)
        self.assertIn("kubernetes-sigs/kueue", res.stderr)

    def test_worktree_add_list_remove(self):
        path = self.run_script(KUEUE_WORKTREE, "add", self.base, "--repo", self.repo).stdout.strip()
        self.assertTrue(path.startswith(str(self.dir / "wt")))
        self.assertEqual(git(path, "rev-parse", "HEAD"), self.base)
        self.assertEqual(git(self.repo, "rev-parse", "HEAD"), self.head)  # PR tree untouched
        listed = self.run_script(KUEUE_WORKTREE, "list").stdout.split()
        self.assertEqual(listed, [path])
        self.run_script(KUEUE_WORKTREE, "remove", path)
        self.assertFalse(Path(path).exists())
        self.assertNotIn(path, git(self.repo, "worktree", "list"))

    def test_worktree_remove_refuses_outside_root(self):
        res = self.run_script(KUEUE_WORKTREE, "remove", self.repo, check=False)
        self.assertEqual(res.returncode, 2)
        self.assertTrue(self.repo.exists())

    def test_worktree_gc(self):
        self.run_script(KUEUE_WORKTREE, "add", self.base, "--repo", self.repo)
        self.run_script(KUEUE_WORKTREE, "add", self.head, "--repo", self.repo)
        self.run_script(KUEUE_WORKTREE, "gc", "--repo", self.repo)
        self.assertEqual(self.run_script(KUEUE_WORKTREE, "list").stdout, "")


if __name__ == "__main__":
    unittest.main()
