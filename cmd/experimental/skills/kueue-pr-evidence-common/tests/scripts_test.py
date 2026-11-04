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

    def test_merge_base_no_fetch_needs_tracking_ref(self):
        git(self.repo, "update-ref", "-d", "refs/remotes/upstream/main")
        (self.repo / ".git" / "FETCH_HEAD").write_text(self.head + "\n")  # stale fetch result
        res = self.run_script(MERGE_BASE, "--worktree", self.repo, "--no-fetch", check=False)
        self.assertEqual(res.returncode, 2)
        self.assertIn("refs/remotes/upstream/main not found", res.stderr)

    def test_worktree_relative_root(self):
        self.env["PR_EVIDENCE_WORKTREE_ROOT"] = "wt-rel"
        res = subprocess.run([str(KUEUE_WORKTREE), "add", self.base, "--repo", str(self.repo)],
                             capture_output=True, text=True, env=self.env, cwd=self.dir, check=True)
        path = res.stdout.strip()
        self.assertTrue(path.startswith(str(self.dir.resolve() / "wt-rel")) or path.startswith(str(self.dir / "wt-rel")), path)
        subprocess.run([str(KUEUE_WORKTREE), "remove", path], capture_output=True, text=True,
                       env=self.env, cwd=self.dir, check=True)
        self.assertFalse(Path(path).exists())

    def test_merge_base_url_match_is_anchored(self):
        git(self.repo, "remote", "set-url", "upstream", "https://evilgithub.com/kubernetes-sigs/kueue.git")
        res = self.run_script(MERGE_BASE, "--worktree", self.repo, "--no-fetch", check=False)
        self.assertEqual(res.returncode, 2)
        for url in ("git@github.com:kubernetes-sigs/kueue.git", "ssh://git@github.com/kubernetes-sigs/kueue",
                    "https://github.com/kubernetes-sigs/kueue/"):
            git(self.repo, "remote", "set-url", "upstream", url)
            self.run_script(MERGE_BASE, "--worktree", self.repo, "--no-fetch")

    def test_merge_base_output_is_eval_safe(self):
        name = "a;touch${IFS}injected"
        git(self.repo, "remote", "add", name, "https://github.com/kubernetes-sigs/kueue.git")
        git(self.repo, "update-ref", f"refs/remotes/{name}/main", self.base)
        self.env["PR_EVIDENCE_UPSTREAM_REMOTE"] = name
        out = self.run_script(MERGE_BASE, "--worktree", self.repo, "--no-fetch").stdout
        res = subprocess.run(["bash", "-c", f'eval "$1"; printf %s "$UPSTREAM_REMOTE"', "_", out],
                             capture_output=True, text=True, cwd=self.dir, check=True)
        self.assertEqual(res.stdout, name)
        self.assertFalse((self.dir / "injected").exists())

    def test_merge_base_fetch_updates_tracking_ref(self):
        up = self.dir / "up"
        git(self.dir, "clone", "-q", "--bare", str(self.repo), str(up))
        git(up, "update-ref", "refs/heads/main", self.head)  # upstream moved on
        git(self.repo, "remote", "add", "local", str(up))
        git(self.repo, "config", "remote.local.fetch", "+refs/heads/other:refs/remotes/local/other")
        git(self.repo, "update-ref", "refs/remotes/local/main", self.base)  # stale
        self.env["PR_EVIDENCE_UPSTREAM_REMOTE"] = "local"
        kv = dict(line.split("=", 1) for line in self.run_script(MERGE_BASE, "--worktree", self.repo).stdout.splitlines())
        self.assertEqual(kv["MERGE_BASE"], self.head)

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
        res = self.run_script(KUEUE_WORKTREE, "gc", "--repo", self.repo)
        self.assertEqual(len(self.run_script(KUEUE_WORKTREE, "list").stdout.split()), 2)  # locked: in use
        self.assertIn("skipped", res.stderr)
        self.run_script(KUEUE_WORKTREE, "gc", "--repo", self.repo, "--force")
        self.assertEqual(self.run_script(KUEUE_WORKTREE, "list").stdout, "")
        self.assertEqual(len(git(self.repo, "worktree", "list").splitlines()), 1)

    def test_worktree_gc_removes_unlocked(self):
        path = self.run_script(KUEUE_WORKTREE, "add", self.base, "--repo", self.repo).stdout.strip()
        git(self.repo, "worktree", "unlock", path)
        self.run_script(KUEUE_WORKTREE, "gc", "--repo", self.repo)
        self.assertEqual(self.run_script(KUEUE_WORKTREE, "list").stdout, "")


if __name__ == "__main__":
    unittest.main()
