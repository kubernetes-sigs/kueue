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

"""Run promote_pull.sh with stub gh, gcloud, make and cosign on PATH.

The script runs in temporary copies of the Kueue and k8s.io repositories whose
upstream remotes are local bare repositories, so no network access is needed.
Requires git, bash, jq and python3.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import provenance

REPO = Path(__file__).resolve().parents[2]
BASH = shutil.which("bash")
VERSION = "v0.21.0"
LIGHTWEIGHT_VERSION = "v0.22.0"
DIGEST = "sha256:" + "d" * 64
REGISTRY = "us-central1-docker.pkg.dev/k8s-staging-images/kueue"
IMAGES_YAML = "registry.k8s.io/images/k8s-staging-kueue/images.yaml"
OTHER_COMMIT = "b" * 40
IDENTITY = "kueue-sa@k8s-staging-images.iam.gserviceaccount.com"

GH_STUB = '''
import sys
if sys.argv[1:3] == ["issue", "list"]:
    print("7\\tRelease v0.21.0")
elif sys.argv[1:3] == ["issue", "view"]:
    print('{"body": "release notes"}')
'''
MAKE_STUB = '''
import shutil
import sys
from pathlib import Path
# The script runs: make -C <kueue repo> yq
bin_dir = Path(sys.argv[2]) / "bin"
bin_dir.mkdir(exist_ok=True)
shutil.copy(Path(__file__).with_name("yq"), bin_dir / "yq")
'''
YQ_STUB = '''
import re
import sys
# The script runs: yq e '.[] | .name' <images.yaml>
for line in open(sys.argv[-1]):
    match = re.match(r"- name: (\\S+)", line)
    if match:
        print(match.group(1))
'''
GCLOUD_STUB = '''
import json
import os
print(json.dumps({"image_summary": {"digest": os.environ["DIGEST"]}}))
'''
# Each call copies the promoter manifest, so a test can see the working tree at every verification.
COSIGN_STUB = '''
import base64
import json
import os
import sys
from pathlib import Path
ref = sys.argv[-1]
snapshots = Path(os.environ["SNAPSHOT_DIR"])
count = len(list(snapshots.iterdir()))
(snapshots / f"{count:03d}").write_bytes(Path(os.environ["IMAGES_FILE"]).read_bytes())
with open(os.environ["STUB_LOG"], "a") as log:
    log.write(" ".join(sys.argv[1:]) + "\\n")
if os.environ.get("COSIGN_NONE"):
    print("no matching attestations", file=sys.stderr)
    sys.exit(1)
statements = Path(os.environ["STATEMENT_DIR"])

def envelope(name):
    payload = (statements / f"{name}.json").read_bytes()
    return json.dumps({"payloadType": "application/vnd.in-toto+json",
                       "payload": base64.b64encode(payload).decode()})

def matches(variable):
    needle = os.environ.get(variable)
    return bool(needle) and needle in ref

if matches("COSIGN_BAD_REF"):
    names = ["stale"]
elif matches("COSIGN_MIXED_REF"):
    names = ["stale", "good"]
elif matches("COSIGN_MALFORMED_REF"):
    names = ["malformed", "good"]
elif matches("COSIGN_CRASH_REF"):
    names = ["good", "crash"]
else:
    names = ["good"]
for name in names:
    print(envelope(name))
'''
# Stands in for timeout. The provenance check of the crash statement exits with CHECK_EXIT;
# other commands run with their stdin and exit code passed through.
TIMEOUT_STUB = '''
import base64
import os
import subprocess
import sys
command = sys.argv[2:]
if command[2:3] == ["check"]:
    payload = sys.stdin.buffer.read()
    if base64.b64decode(payload).strip() == b"crash":
        sys.exit(int(os.environ["CHECK_EXIT"]))
    sys.exit(subprocess.run(command, input=payload).returncode)
sys.exit(subprocess.run(command).returncode)
'''
# Stands in for gtimeout. It logs its arguments, then runs the command.
GTIMEOUT_STUB = '''
import os
import sys
with open(os.environ["TIMEOUT_LOG"], "a") as log:
    log.write(" ".join(sys.argv[1:]) + "\\n")
os.execvp(sys.argv[2], sys.argv[2:])
'''


class PromotePullTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = {
            **os.environ,
            "GIT_CONFIG_GLOBAL": str(self.root / "gitconfig"),
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_TERMINAL_PROMPT": "0",
            "GIT_AUTHOR_NAME": "Release test",
            "GIT_AUTHOR_EMAIL": "release-test@example.com",
            "GIT_COMMITTER_NAME": "Release test",
            "GIT_COMMITTER_EMAIL": "release-test@example.com",
            "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            "GITHUB_USER": "tester",
            "DIGEST": DIGEST,
            "STUB_LOG": str(self.root / "cosign.log"),
            "SNAPSHOT_DIR": str(self.root / "snapshots"),
            "KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG": "kubernetes-sigs",
            "KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME": "kueue",
            "KUBERNETES_K8S_IO_MAIN_REPO_ORG": "kubernetes",
            "KUBERNETES_K8S_IO_MAIN_REPO_NAME": "k8s.io",
        }
        Path(self.env["SNAPSHOT_DIR"]).mkdir()
        for name, body in {"gh": GH_STUB, "make": MAKE_STUB, "gcloud": GCLOUD_STUB,
                           "yq": YQ_STUB, "cosign": COSIGN_STUB}.items():
            self.write_executable(self.bin / name, body)
        self.kueue = self.root / "kueue"
        self.make_kueue_repo()
        self.statements = self.root / "statements"
        self.statements.mkdir()
        (self.statements / "good.json").write_text(self.statement(self.commit))
        (self.statements / "stale.json").write_text(self.statement(OTHER_COMMIT))
        (self.statements / "malformed.json").write_text("not json")
        (self.statements / "crash.json").write_text("crash")
        self.lightweight = self.root / "lightweight"
        self.lightweight.mkdir()
        (self.lightweight / "good.json").write_text(self.statement(self.commit, LIGHTWEIGHT_VERSION))
        self.k8s_io = self.root / "k8s.io"
        self.make_k8s_io_repo()
        self.original_images = (self.k8s_io / IMAGES_YAML).read_bytes()
        self.env.update(STATEMENT_DIR=str(self.statements),
                        IMAGES_FILE=str(self.k8s_io / IMAGES_YAML),
                        KUBERNETES_K8S_IO_PATH=str(self.k8s_io))

    def write_executable(self, path, body):
        path.write_text(f"#!{sys.executable}\n" + body)
        path.chmod(0o755)

    def git(self, cwd, *args):
        return subprocess.run(["git", *args], cwd=cwd, env=self.env,
                              capture_output=True, text=True, check=True).stdout.strip()

    def statement(self, commit, version=VERSION):
        predicate = provenance.predicate(version, commit, "build-1", "k8s-staging-images", "123")
        return json.dumps({
            "_type": "https://in-toto.io/Statement/v1",
            "subject": [{"name": "image", "digest": {"sha256": "d" * 64}}],
            "predicateType": provenance.PREDICATE_TYPE,
            "predicate": predicate,
        })

    def make_kueue_repo(self):
        # The script copies the files it runs from this repository.
        self.git(self.root, "init", str(self.kueue))
        for relative in ["hack/utils.sh", "hack/releasing/promote_pull.sh", "hack/releasing/provenance.py"]:
            target = self.kueue / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(REPO / relative, target)
        self.git(self.kueue, "add", ".")
        self.git(self.kueue, "commit", "-m", "Release commit")
        self.commit = self.git(self.kueue, "rev-parse", "HEAD")
        self.git(self.kueue, "tag", "-a", VERSION, "-m", "Release tag")
        self.git(self.kueue, "tag", LIGHTWEIGHT_VERSION)
        # Later commit with an annotated rc tag: a prefix match that must not be used for v0.21.0.
        self.git(self.kueue, "commit", "--allow-empty", "-m", "Later commit")
        self.git(self.kueue, "tag", "-a", f"{VERSION}-rc.1", "-m", "Release candidate")
        upstream = self.root / "kueue-upstream.git"
        self.git(self.root, "init", "--bare", str(upstream))
        self.git(self.kueue, "remote", "add", "upstream", str(upstream))
        self.git(self.kueue, "push", "upstream", "HEAD:refs/heads/main", f"refs/tags/{VERSION}",
                 f"refs/tags/{LIGHTWEIGHT_VERSION}", f"refs/tags/{VERSION}-rc.1")

    def make_k8s_io_repo(self):
        self.git(self.root, "init", str(self.k8s_io))
        self.git(self.k8s_io, "symbolic-ref", "HEAD", "refs/heads/main")
        images = self.k8s_io / IMAGES_YAML
        images.parent.mkdir(parents=True)
        images.write_text(
            "- name: kueue\n"
            f'  dmap:\n    "sha256:{"1" * 64}": ["v0.20.0"]\n'
            "- name: charts/kueue\n"
            f'  dmap:\n    "sha256:{"2" * 64}": ["0.20.0"]\n'
        )
        self.git(self.k8s_io, "add", ".")
        self.git(self.k8s_io, "commit", "-m", "Images")
        upstream = self.root / "k8s-io-upstream.git"
        self.git(self.root, "init", "--bare", str(upstream))
        self.git(self.k8s_io, "remote", "add", "upstream", str(upstream))
        self.git(self.k8s_io, "push", "upstream", "HEAD:refs/heads/main")
        self.git(self.k8s_io, "fetch", "upstream")
        self.main_tip = self.git(self.k8s_io, "rev-parse", "upstream/main")

    def path_without(self, *names):
        # Symlink every executable from the stubs and PATH except the named ones, so none of them is found.
        links = self.root / ("no-" + "-".join(names))
        links.mkdir()
        for directory in [self.bin, *map(Path, os.environ["PATH"].split(os.pathsep))]:
            try:
                entries = list(directory.iterdir())
            except OSError:
                continue
            for entry in entries:
                if entry.name in names or (links / entry.name).exists():
                    continue
                try:
                    runnable = entry.is_file() and os.access(entry, os.X_OK)
                except OSError:
                    continue
                if runnable:
                    (links / entry.name).symlink_to(entry)
        return str(links)

    def run_script(self, *, version=VERSION, check_provenance=True, dry_run=True):
        env = {**self.env}
        if dry_run:
            env["DRY_RUN"] = "1"
        else:
            env.pop("DRY_RUN", None)
        command = [BASH, str(self.kueue / "hack/releasing/promote_pull.sh")]
        if check_provenance:
            command.append("--check-provenance")
        return subprocess.run([*command, version], cwd=self.kueue, env=env,
                              capture_output=True, text=True)

    def promote_branches(self):
        return self.git(self.k8s_io, "for-each-ref", "--format=%(refname)",
                        "refs/heads/kueue-promote-*").splitlines()

    def promote_branch_tip(self):
        branches = self.promote_branches()
        self.assertEqual(len(branches), 1, branches)
        return self.git(self.k8s_io, "rev-parse", branches[0])

    def manifest_at(self, commit):
        return self.git(self.k8s_io, "show", f"{commit}:{IMAGES_YAML}")

    def output(self, result):
        return result.stdout + result.stderr

    def bang_lines(self, result):
        return [line for line in self.output(result).splitlines() if line.startswith("!!! ")]

    def assert_images_unchanged(self):
        snapshots = sorted((self.root / "snapshots").iterdir())
        self.assertTrue(snapshots, "cosign was never called")
        for snapshot in snapshots:
            self.assertEqual(snapshot.read_bytes(), self.original_images,
                             f"manifest changed before verification {snapshot.name}")

    def assert_failed_without_changes(self, result):
        self.assertNotEqual(result.returncode, 0, self.output(result))
        self.assertEqual(self.promote_branches(), [], "promote branch left behind")
        self.assertEqual(self.git(self.k8s_io, "symbolic-ref", "--short", "HEAD"), "main")
        self.assert_images_unchanged()

    def test_pass_verifies_every_digest_then_records_them(self):
        result = self.run_script()

        self.assertEqual(result.returncode, 0, self.output(result))
        calls = Path(self.env["STUB_LOG"]).read_text().splitlines()
        self.assertEqual(len(calls), 2)
        for call in calls:
            self.assertIn("--type slsaprovenance1", call)
            self.assertIn(f"--certificate-identity {IDENTITY}", call)
            self.assertIn("--certificate-oidc-issuer https://accounts.google.com", call)
        for name in ["kueue", "charts/kueue"]:
            self.assertTrue(any(call.endswith(f"{REGISTRY}/{name}@{DIGEST}") for call in calls), name)
        self.assertEqual(self.manifest_at(self.promote_branch_tip()).count(DIGEST), 2)
        self.assert_images_unchanged()

    def test_stale_attestation_next_to_matching_one_passes(self):
        self.env["COSIGN_MIXED_REF"] = "charts/kueue"
        result = self.run_script()

        self.assertEqual(result.returncode, 0, self.output(result))
        self.assertEqual(self.manifest_at(self.promote_branch_tip()).count(DIGEST), 2)

    def test_bad_digest_stops_before_any_digest_is_recorded(self):
        # The first image passes, so only verifying every digest first keeps it out of the manifest.
        self.env["COSIGN_BAD_REF"] = "charts/kueue"
        result = self.run_script()

        self.assert_failed_without_changes(result)
        pattern = (rf"^!!! Provenance of {re.escape(REGISTRY)}/charts/kueue:0\.21\.0@{re.escape(DIGEST)}"
                   rf" does not match commit {self.commit}: .*NOT_READY")
        self.assertTrue(any(re.search(pattern, line) for line in self.bang_lines(result)),
                        self.output(result))

    def test_any_failing_attestation_fails_the_digest(self):
        # A passing attestation next to a malformed one must still fail the digest.
        self.env["COSIGN_MALFORMED_REF"] = "charts/kueue"
        result = self.run_script()

        self.assert_failed_without_changes(result)
        self.assertTrue(any("does not match commit" in line and "malformed payload" in line
                            for line in self.bang_lines(result)), self.output(result))

    def test_missing_attestation_stops_the_script(self):
        self.env["COSIGN_NONE"] = "1"
        result = self.run_script()

        self.assert_failed_without_changes(result)
        self.assertTrue(any("No verified attestation" in line and "no matching attestations" in line
                            for line in self.bang_lines(result)), self.output(result))

    def test_annotated_tag_peels_to_its_commit_not_a_prefix_match(self):
        # v0.21.0-rc.1 sits on a later commit; the statement must match the v0.21.0 commit.
        result = self.run_script()

        self.assertEqual(result.returncode, 0, self.output(result))
        self.assertIn(f"at commit {self.commit}", self.output(result))

    def test_lightweight_tag_commit_is_used(self):
        self.env["STATEMENT_DIR"] = str(self.lightweight)
        result = self.run_script(version=LIGHTWEIGHT_VERSION)

        self.assertEqual(result.returncode, 0, self.output(result))
        self.assertIn(f"at commit {self.commit}", self.output(result))

    def test_unresolvable_tag_reports_could_not_resolve(self):
        result = self.run_script(version="v0.99.0")

        self.assertNotEqual(result.returncode, 0, self.output(result))
        self.assertIn("Could not resolve release tag v0.99.0", self.output(result))
        self.assertFalse(Path(self.env["STUB_LOG"]).exists())

    def test_unreachable_remote_reports_could_not_resolve(self):
        self.env["KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE"] = "missing"
        result = self.run_script()

        self.assertNotEqual(result.returncode, 0, self.output(result))
        self.assertIn(f"Could not resolve release tag {VERSION}", self.output(result))

    def test_missing_cosign_stops_before_any_change(self):
        self.env["PATH"] = self.path_without("cosign")
        self.assertIsNone(shutil.which("cosign", path=self.env["PATH"]))
        result = self.run_script()

        self.assertEqual(result.returncode, 1, self.output(result))
        self.assertIn("Can't find 'cosign' tool in PATH", self.output(result))
        self.assertEqual(self.promote_branches(), [])

    def test_flag_absent_skips_cosign(self):
        result = self.run_script(check_provenance=False)

        self.assertEqual(result.returncode, 0, self.output(result))
        self.assertFalse(Path(self.env["STUB_LOG"]).exists())
        self.assertEqual(self.manifest_at(self.promote_branch_tip()).count(DIGEST), 2)

    def test_failure_without_dry_run_removes_the_promote_branch(self):
        self.env["COSIGN_BAD_REF"] = "charts/kueue"
        result = self.run_script(dry_run=False)

        self.assert_failed_without_changes(result)

    def test_crashed_check_next_to_passing_one_fails_the_digest(self):
        # A check that timed out or could not run is not a pass, even next to a passing attestation.
        self.env["COSIGN_CRASH_REF"] = "charts/kueue"
        self.write_executable(self.bin / "timeout", TIMEOUT_STUB)
        for code in (124, 127):
            with self.subTest(code=code):
                self.env["CHECK_EXIT"] = str(code)
                result = self.run_script()

                self.assert_failed_without_changes(result)
                self.assertTrue(any("does not match commit" in line and f"exit {code}" in line
                                    for line in self.bang_lines(result)), self.output(result))

    def test_gtimeout_is_used_without_timeout(self):
        gtimeout_dir = self.root / "gtimeout"
        gtimeout_dir.mkdir()
        self.write_executable(gtimeout_dir / "gtimeout", GTIMEOUT_STUB)
        self.env["PATH"] = f"{gtimeout_dir}{os.pathsep}{self.path_without('timeout', 'gtimeout')}"
        self.env["TIMEOUT_LOG"] = str(self.root / "timeout.log")
        result = self.run_script()

        self.assertEqual(result.returncode, 0, self.output(result))
        log = Path(self.env["TIMEOUT_LOG"])
        calls = log.read_text().splitlines() if log.exists() else []
        self.assertTrue(any(call.startswith("120 cosign ") for call in calls), calls)
        self.assertTrue(any(call.startswith("60 git ") for call in calls), calls)

    def test_no_timeout_tool_warns_and_runs(self):
        self.env["PATH"] = self.path_without("timeout", "gtimeout")
        result = self.run_script()

        self.assertEqual(result.returncode, 0, self.output(result))
        self.assertEqual(self.output(result).count("neither timeout nor gtimeout found"), 1, self.output(result))
        self.assertTrue(Path(self.env["STUB_LOG"]).exists())


if __name__ == "__main__":
    unittest.main()
