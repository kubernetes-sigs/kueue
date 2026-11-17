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

"""Run hack/attest-artifacts.sh against fixture digests and a fake cosign."""

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "hack" / "attest-artifacts.sh"
REGISTRY = "us-central1-docker.pkg.dev/k8s-staging-images/kueue"
SOURCE_URI_PREFIX = "git+https://github.com/kubernetes-sigs/kueue"
IMAGES = ("kueue", "kueueviz-backend", "kueueviz-frontend", "kueue-populator", "kueue-priority-booster")
CHARTS = ("kueue", "kueue-populator", "kueue-priority-booster")
ARTIFACT_COUNT = len(IMAGES) + len(CHARTS)

# Fails the test if the dry run ever calls cosign.
FAILING_COSIGN = """#!/bin/bash
echo "cosign $*" >> "$COSIGN_LOG"
exit 99
"""

# Stands in for cosign with the attest and verify-attestation behavior the script uses.
# It records each call in COSIGN_LOG. verify-attestation prints one DSSE envelope per
# line for the artifact named by the reference, built from the predicate the script wrote.
# FAKE_MODE selects the failure: attest-fail, mismatch (every envelope has another
# predicate) or stale (an older envelope comes before the current one).
FAKE_COSIGN = """#!{python}
import base64, json, os, sys
args = sys.argv[1:]
with open(os.environ["COSIGN_LOG"], "a", encoding="utf-8") as log:
    log.write(json.dumps(args) + "\\n")
mode = os.environ.get("FAKE_MODE", "")
if args[0] == "attest":
    sys.exit(1 if mode == "attest-fail" else 0)
if args[0] != "verify-attestation":
    sys.exit(3)
repo, digest = args[-1].split("@", 1)
label = ("chart-" if "/charts/" in repo else "image-") + repo.rsplit("/", 1)[-1]
with open(os.path.join(os.environ["DIGEST_DIR"], "predicates", label + ".predicate.json"), encoding="utf-8") as f:
    predicate = json.load(f)


def envelope(predicate):
    statement = {
        "_type": "https://in-toto.io/Statement/v1",
        "subject": [{"name": repo, "digest": {"sha256": digest.split(":", 1)[1]}}],
        "predicateType": "https://slsa.dev/provenance/v1",
        "predicate": predicate,
    }
    return {
        "payloadType": "application/vnd.in-toto+json",
        "payload": base64.b64encode(json.dumps(statement).encode()).decode(),
        "signatures": [{"keyid": "", "sig": "fake"}],
    }


older = json.loads(json.dumps(predicate))
older["runDetails"]["metadata"]["invocationId"] += "-older-build"
if mode == "mismatch":
    envelopes = [envelope(older)]
elif mode == "stale":
    envelopes = [envelope(older), envelope(predicate)]
else:
    envelopes = [envelope(predicate)]
for item in envelopes:
    print(json.dumps(item))
""".replace("{python}", sys.executable)


def fixture_digest(name):
    return "sha256:" + hashlib.sha256(name.encode()).hexdigest()


def head_commit():
    return subprocess.run(
        ["git", "-C", str(ROOT), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()


# A minimal cloudbuild.yaml with one attest step. {allow} is the allowFailure line, and {sign} is the _SIGN default.
CLOUDBUILD_TEMPLATE = """steps:
  - name: 'gcr.io/k8s-staging-test-infra/gcb-docker-gcloud:v20260127-c1affcc8de@sha256:8e58105524e23875214a02f615ee9cab9a97b3f9e4b8e9da54726615758e1285'
    entrypoint: bash
    args:
      - hack/attest-artifacts.sh
    env:
      - SIGN=${_SIGN}
{allow}
substitutions:
  _SIGN: '{sign}'
"""


def allow_failure_problems(cloudbuild_text):
    """Return why the attest step's allowFailure does not match the _SIGN default, or []."""
    build = yaml.safe_load(cloudbuild_text)
    if not isinstance(build, dict):
        return ["cloudbuild.yaml must be a mapping"]
    sign = (build.get("substitutions") or {}).get("_SIGN")
    if sign not in ("true", "false"):
        return [f"_SIGN default must be 'true' or 'false', got {sign!r}"]
    steps = [s for s in (build.get("steps") or [])
             if isinstance(s, dict) and isinstance(s.get("args"), list)
             and "hack/attest-artifacts.sh" in s["args"]]
    if len(steps) != 1:
        return [f"exactly one step must run hack/attest-artifacts.sh, found {len(steps)}"]
    step = steps[0]
    problems = []
    if "SIGN=${_SIGN}" not in (step.get("env") or []):
        problems.append("attest step must pass SIGN=${_SIGN}")
    allow = step.get("allowFailure", False)
    if not isinstance(allow, bool):
        problems.append(f"allowFailure must be a boolean, got {allow!r}")
    elif allow != (sign == "false"):
        problems.append("allowFailure must be true exactly while _SIGN defaults to 'false'")
    return problems


class AttestArtifactsTest(unittest.TestCase):
    def setUp(self):
        self.directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.digest_dir = self.directory / "digests"
        self.digest_dir.mkdir()
        for name in IMAGES:
            (self.digest_dir / f"{name}.json").write_text(
                json.dumps({"containerimage.digest": fixture_digest(name)}) + "\n")
        for name in CHARTS:
            (self.digest_dir / f"chart-{name}.digest").write_text(fixture_digest("chart-" + name) + "\n")

        self.bin_dir = self.directory / "bin"
        self.bin_dir.mkdir()
        failing = self.bin_dir / "cosign"
        failing.write_text(FAILING_COSIGN)
        failing.chmod(0o755)

        self.fake_cosign = self.directory / "fake-cosign"
        self.fake_cosign.write_text(FAKE_COSIGN)
        self.fake_cosign.chmod(0o755)
        self.cosign_log = self.directory / "cosign.log"

    def run_script(self, sign="false", ref="main", digest_dir=None, extra=None):
        env = {key: value for key, value in os.environ.items()
               if key not in ("DIGEST_DIR", "PREDICATE_DIR", "COSIGN_BIN", "TMPDIR")}
        env.update({
            "PATH": f"{self.bin_dir}{os.pathsep}{os.environ['PATH']}",
            "COSIGN_LOG": str(self.cosign_log),
            "DIGEST_DIR": str(digest_dir or self.digest_dir),
            "SIGN": sign,
            "PULL_BASE_REF": ref,
            "BUILD_ID": "build-123",
            "PROJECT_ID": "k8s-staging-images",
            "PROJECT_NUMBER": "123456789",
        })
        env.update(extra or {})
        return subprocess.run(["bash", str(SCRIPT)], env=env, text=True, capture_output=True)

    def run_signed(self, ref="main", mode=""):
        """Run with SIGN=true and the fake cosign in place of the pinned download."""
        return self.run_script(sign="true", ref=ref, extra={
            "COSIGN_BIN": str(self.fake_cosign),
            "FAKE_MODE": mode,
        })

    def cosign_calls(self, subcommand):
        if not self.cosign_log.exists():
            return []
        calls = [json.loads(line) for line in self.cosign_log.read_text().splitlines()]
        return [call for call in calls if call[0] == subcommand]

    def predicates(self):
        """Return the predicate written for each artifact label, keyed by label."""
        return {
            path.name[:-len(".predicate.json")]: json.loads(path.read_text())
            for path in (self.digest_dir / "predicates").glob("*.predicate.json")
        }

    def test_dry_run_writes_one_predicate_per_artifact(self):
        """Write one predicate for each of the 5 images and 3 charts, and call no cosign."""
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        predicates = self.predicates()
        expected = {f"image-{name}" for name in IMAGES} | {f"chart-{name}" for name in CHARTS}
        self.assertEqual(set(predicates), expected)
        commit = head_commit()
        for label, predicate in predicates.items():
            with self.subTest(label=label):
                source = predicate["buildDefinition"]["externalParameters"]["source"]
                self.assertEqual(source["uri"], f"{SOURCE_URI_PREFIX}@refs/heads/main")
                self.assertEqual(source["digest"]["gitCommit"], commit)
        self.assertFalse(self.cosign_log.exists(), "dry run must not call cosign")

    def test_dry_run_logs_each_staging_reference_with_its_digest(self):
        """Log the image and chart paths that the push used, pinned to the recorded digests."""
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for name in IMAGES:
            self.assertIn(f"{REGISTRY}/{name}@{fixture_digest(name)}", result.stdout)
        for name in CHARTS:
            self.assertIn(f"{REGISTRY}/charts/{name}@{fixture_digest('chart-' + name)}", result.stdout)

    def test_tag_build_records_the_release_tag_ref(self):
        """Record refs/tags/<tag> when the build was triggered by a release tag."""
        result = self.run_script(ref="v0.21.0")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for label, predicate in self.predicates().items():
            with self.subTest(label=label):
                source = predicate["buildDefinition"]["externalParameters"]["source"]
                self.assertEqual(source["uri"], f"{SOURCE_URI_PREFIX}@refs/tags/v0.21.0")

    def test_missing_artifact_fails_before_writing_predicates(self):
        """Fail when one expected chart digest is missing, and write no predicate."""
        (self.digest_dir / "chart-kueue-populator.digest").unlink()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing chart digest file", result.stderr)
        self.assertIn("chart-kueue-populator.digest", result.stderr)
        self.assertFalse((self.digest_dir / "predicates").exists())

    def test_missing_image_digest_fails(self):
        """Fail when an image metadata file is missing."""
        (self.digest_dir / "kueueviz-frontend.json").unlink()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing image digest file", result.stderr)
        self.assertIn("kueueviz-frontend.json", result.stderr)

    def test_invalid_sign_value_fails(self):
        """Reject any SIGN value other than true or false."""
        result = self.run_script(sign="yes")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SIGN must be true or false", result.stderr)
        self.assertFalse(self.cosign_log.exists())

    def test_signed_tag_build_attests_and_verifies_every_artifact(self):
        """Attest and verify each of the 8 artifacts for a release tag build."""
        result = self.run_signed(ref="v0.21.0")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        attests = self.cosign_calls("attest")
        verifies = self.cosign_calls("verify-attestation")
        self.assertEqual(len(attests), ARTIFACT_COUNT)
        self.assertEqual(len(verifies), ARTIFACT_COUNT)
        for call in attests:
            with self.subTest(call=call):
                self.assertIn("--type", call)
                self.assertEqual(call[call.index("--type") + 1], "slsaprovenance1")
                self.assertIn("--predicate", call)
        for call in verifies:
            with self.subTest(call=call):
                self.assertIn("--certificate-identity", call)
                self.assertEqual(call[call.index("--certificate-identity") + 1],
                                 "kueue-sa@k8s-staging-images.iam.gserviceaccount.com")
                self.assertEqual(call[call.index("--certificate-oidc-issuer") + 1],
                                 "https://accounts.google.com")
        self.assertEqual(len(self.predicates()), ARTIFACT_COUNT)

    def test_signed_branch_build_passes(self):
        """Pass the self-check for a branch build, which the release-tag check would reject."""
        result = self.run_signed(ref="main")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.cosign_calls("verify-attestation")), ARTIFACT_COUNT)

    def test_signed_predicate_mismatch_fails(self):
        """Fail when the verified attestation carries a predicate from another build."""
        result = self.run_signed(mode="mismatch")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("none of 1 verified attestation(s)", result.stderr)

    def test_signed_stale_attestation_next_to_current_passes(self):
        """Pass when a digest has a stale attestation from an earlier build and the current one."""
        result = self.run_signed(mode="stale")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.cosign_calls("verify-attestation")), ARTIFACT_COUNT)

    def test_signed_cosign_attest_failure_exits_non_zero(self):
        """Fail the build when cosign attest fails, and do not verify that artifact."""
        result = self.run_signed(mode="attest-fail")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cosign attest failed for image-kueue", result.stderr)
        self.assertEqual(self.cosign_calls("verify-attestation"), [])

    def test_signed_run_removes_its_temp_dir_on_success_and_failure(self):
        """Remove the work directory on exit, whether the run passes or fails."""
        for mode in ("", "attest-fail"):
            with self.subTest(mode=mode):
                tmp = self.directory / f"tmp-{mode or 'pass'}"
                tmp.mkdir()
                self.cosign_log.unlink(missing_ok=True)
                self.run_script(sign="true", extra={
                    "COSIGN_BIN": str(self.fake_cosign),
                    "FAKE_MODE": mode,
                    "TMPDIR": str(tmp),
                })
                self.assertEqual(list(tmp.iterdir()), [])

    def test_allow_failure_only_while_signing_is_off(self):
        """Keep allowFailure on the attestation step only while _SIGN defaults to false."""
        text = (ROOT / "cloudbuild.yaml").read_text()
        build = yaml.safe_load(text)
        self.assertEqual(allow_failure_problems(text), [])
        if build["substitutions"]["_SIGN"] == "false":
            self.assertRegex(text, r"TODO\(KEP-1466\).*\ballowFailure\b",
                             "allowFailure needs a TODO(KEP-1466) removal marker")

    def test_allow_failure_check_rejects_mismatched_steps(self):
        """Reject allowFailure values that do not match the _SIGN default."""
        cases = [
            ("    allowFailure: true  # dry run only", "true", False),
            ("    allowFailure:   true", "true", False),
            ("", "false", False),
            ('    allowFailure: "true"', "false", False),
            ("", "true", True),
            ("    allowFailure: true  # comment", "false", True),
        ]
        for allow, sign, valid in cases:
            with self.subTest(allow=allow, sign=sign):
                text = CLOUDBUILD_TEMPLATE.replace("{allow}", allow).replace("{sign}", sign)
                self.assertEqual(allow_failure_problems(text) == [], valid)

    def test_allow_failure_check_reports_malformed_attest_steps(self):
        """Report a problem for an invalid _SIGN default, a missing SIGN env, or two attest steps."""
        base = CLOUDBUILD_TEMPLATE.replace("{allow}", "    allowFailure: true").replace("{sign}", "false")
        steps, rest = base.split("substitutions:", 1)
        duplicated = steps + steps[len("steps:\n"):] + "substitutions:" + rest
        cases = [
            ("_SIGN default maybe", base.replace("_SIGN: 'false'", "_SIGN: 'maybe'")),
            ("missing SIGN env", base.replace("      - SIGN=${_SIGN}\n", "")),
            ("two attest steps", duplicated),
        ]
        for name, text in cases:
            with self.subTest(name):
                self.assertNotEqual(allow_failure_problems(text), [])


if __name__ == "__main__":
    unittest.main()
