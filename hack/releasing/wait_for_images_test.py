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

import base64
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import provenance

SCRIPT = Path(__file__).resolve().parent / "wait_for_images.sh"

TAG = "v0.21.0"
CHART_VERSION = "0.21.0"
COMMIT = "0123456789abcdef0123456789abcdef01234567"
OTHER_COMMIT = "fedcba9876543210fedcba9876543210fedcba98"
# The tag object of an annotated tag. It is not a commit, so a script that does not
# peel the tag never matches a statement that records COMMIT.
TAG_OBJECT = "b" * 40
DIGEST_HEX = "7".rjust(64, "0")
DIGEST = f"sha256:{DIGEST_HEX}"
REGISTRY = "us-central1-docker.pkg.dev/k8s-staging-images/kueue"
IMAGES = ["kueue", "kueueviz-backend", "kueueviz-frontend", "kueue-populator", "kueue-priority-booster"]
CHARTS = ["kueue", "kueue-populator", "kueue-priority-booster"]
# The five images and the three charts, the artifacts attested in KEP-1466.
EXPECTED_REFS = [f"{REGISTRY}/{image}:{TAG}" for image in IMAGES] + [
    f"{REGISTRY}/charts/{chart}:{CHART_VERSION}" for chart in CHARTS
]
IDENTITY = "kueue-sa@k8s-staging-images.iam.gserviceaccount.com"
NEVER = 10**6

# Stubs replace the real tools on PATH. Each records its arguments under STUB_DIR.
STUBS = {
    "crane": f"#!/bin/sh\necho sha256:{DIGEST_HEX}\n",
    "curl": "#!/bin/sh\nexit 1\n",
    "sleep": "#!/bin/sh\nexit 0\n",
    "git": r"""#!/bin/sh
echo "$*" >> "$STUB_DIR/git.calls"
calls=$(wc -l < "$STUB_DIR/git.calls")
if [ "$calls" -gt "$GIT_MOVES_AFTER_CALLS" ]; then commit="$GIT_TAG_AFTER"; else commit="$GIT_TAG_BEFORE"; fi
[ -n "$commit" ] || exit 0
shift
for ref in "$@"; do
  case "$ref" in
    *'^{}')
      if [ "$GIT_ANNOTATED" = 1 ]; then printf '%s\t%s\n' "$commit" "$ref"; fi
      ;;
    *)
      if [ "$GIT_ANNOTATED" = 1 ]; then object="$TAG_OBJECT"; else object="$commit"; fi
      printf '%s\t%s\n' "$object" "$ref"
      ;;
  esac
done
""",
"timeout": r"""#!/bin/sh
echo "$1" >> "$STUB_DIR/timeout.calls"
shift
exec "$@"
""",
    "cosign": r"""#!/bin/sh
echo "$*" >> "$STUB_DIR/cosign.calls"
calls=$(wc -l < "$STUB_DIR/cosign.calls")
if [ "$COSIGN_DELAY" -gt 0 ]; then /bin/sleep "$COSIGN_DELAY"; fi
if [ -n "$COSIGN_FORCED_EXIT" ]; then exit "$COSIGN_FORCED_EXIT"; fi
if [ "$calls" -le "$COSIGN_FAILING_CALLS" ]; then
  echo "Error: no matching attestations found for the given digest" >&2
  exit 1
fi
cat "$ATTESTATION_FILE"
""",
}


def _attestation(commit: str = COMMIT, build_type: str | None = None) -> str:
    """Return one cosign verify-attestation output line for a build of TAG at commit.

    The line has the shape of a DSSE envelope: payloadType, base64 payload and signatures.
    build_type replaces the buildType of the predicate when it is set.
    """
    predicate = provenance.predicate(TAG, commit, "build-123", "k8s-staging-images", "123456789012")
    if build_type is not None:
        predicate["buildDefinition"]["buildType"] = build_type
    statement = {
        "_type": "https://in-toto.io/Statement/v1",
        "subject": [{"name": "kueue", "digest": {"sha256": DIGEST_HEX}}],
        "predicateType": provenance.PREDICATE_TYPE,
        "predicate": predicate,
    }
    payload = base64.b64encode(json.dumps(statement).encode("utf-8")).decode("ascii")
    envelope = {
        "payloadType": "application/vnd.in-toto+json",
        "payload": payload,
        "signatures": [{"keyid": "", "sig": "MEUCIQDdummysignatureforfixture"}],
    }
    return json.dumps(envelope)


class WaitForImagesTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.state = Path(tmp.name) / "state"
        self.state.mkdir()
        self.bin = Path(tmp.name) / "bin"
        self.bin.mkdir()
        for name, body in STUBS.items():
            path = self.bin / name
            path.write_text(body)
            path.chmod(0o755)

    def run_script(
        self,
        args: list[str],
        *,
        attestation: str | None = None,
        attested_commit: str = COMMIT,
        tag_before: str = COMMIT,
        tag_after: str = COMMIT,
        tag_moves_after_calls: int = NEVER,
        annotated: bool = False,
        cosign_failing_calls: int = 0,
        cosign_forced_exit: int | None = None,
        cosign_delay: int = 0,
    ) -> subprocess.CompletedProcess:
        attestation_file = self.state / "attestation.json"
        attestation_file.write_text(attestation or _attestation(attested_commit))
        env = {
            **os.environ,
            "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            "STUB_DIR": str(self.state),
            "ATTESTATION_FILE": str(attestation_file),
            "GIT_TAG_BEFORE": tag_before,
            "GIT_TAG_AFTER": tag_after,
            "GIT_MOVES_AFTER_CALLS": str(tag_moves_after_calls),
            "GIT_ANNOTATED": "1" if annotated else "0",
            "TAG_OBJECT": TAG_OBJECT,
            "COSIGN_FAILING_CALLS": str(cosign_failing_calls),
            "COSIGN_FORCED_EXIT": "" if cosign_forced_exit is None else str(cosign_forced_exit),
            "COSIGN_DELAY": str(cosign_delay),
        }
        return subprocess.run(
            ["bash", str(SCRIPT), *args],
            env=env,
            text=True,
            capture_output=True,
            timeout=120,
        )

    def calls(self, tool: str) -> list[str]:
        path = self.state / f"{tool}.calls"
        if not path.exists():
            return []
        return path.read_text().splitlines()

    def test_passes_when_attestations_record_tag_and_commit(self) -> None:
        result = self.run_script(["--check-provenance", "--timeout", "10s", TAG])
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        cosign = self.calls("cosign")
        self.assertEqual(len(EXPECTED_REFS), len(cosign), cosign)
        for ref in EXPECTED_REFS:
            self.assertTrue(
                any(f"{ref}@{DIGEST}" in call for call in cosign),
                f"no cosign call for {ref}@{DIGEST}",
            )
        for call in cosign:
            self.assertIn("--type slsaprovenance1", call)
            self.assertIn(f"--certificate-identity {IDENTITY}", call)
            self.assertIn("--certificate-oidc-issuer https://accounts.google.com", call)
        # One call resolves the tag for the poll and one rechecks it before the poll passes.
        self.assertEqual(2, len(self.calls("git")))
        self.assertIn("refs/tags/v0.21.0^{}", self.calls("git")[0])

    def test_annotated_tag_uses_peeled_commit(self) -> None:
        result = self.run_script(["--check-provenance", "--timeout", "10s", TAG], annotated=True)
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertEqual(len(EXPECTED_REFS), len(self.calls("cosign")))

    def test_keeps_polling_until_tag_moves(self) -> None:
        result = self.run_script(
            ["--check-provenance", "--timeout", "10s", TAG],
            attested_commit=OTHER_COMMIT,
            tag_before=COMMIT,
            tag_after=OTHER_COMMIT,
            tag_moves_after_calls=2,
        )
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn("NOT_READY", result.stdout)
        self.assertGreaterEqual(len(self.calls("git")), 3)

    def test_keeps_polling_without_verified_attestation(self) -> None:
        result = self.run_script(
            ["--check-provenance", "--timeout", "10s", TAG],
            cosign_failing_calls=1,
        )
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn("No verified SLSA provenance attestation yet: Error: no matching attestations", result.stdout)
        self.assertEqual(1, result.stdout.count("No verified SLSA provenance attestation yet"))

    def test_fails_at_once_on_mismatched_build_type(self) -> None:
        result = self.run_script(
            ["--check-provenance", "--timeout", "10s", TAG],
            attestation=_attestation(build_type="https://example.com/other"),
        )
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        self.assertIn("!!! Error: SLSA provenance check failed for", result.stdout)
        self.assertIn("buildType", result.stdout)
        self.assertEqual(1, len(self.calls("cosign")))
        self.assertNotIn("Try again", result.stdout)

    def test_reports_cosign_timeout_as_reason(self) -> None:
        # The limit is capped to the time left, so it is at most the 3s timeout, not 120s.
        result = self.run_script(
            ["--check-provenance", "--timeout", "3s", TAG],
            cosign_forced_exit=124,
        )
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        # The first poll runs at the start of the budget, so its reason is logged. Polls after
        # the deadline make no calls, so the first poll's reason is kept.
        reason = r"cosign verify-attestation timed out after [1-3]s"
        self.assertRegex(result.stdout, rf"No verified SLSA provenance attestation yet: {reason}")
        error_lines = [line for line in result.stdout.splitlines() if line.startswith("!!! Error: Timed out")]
        self.assertEqual(1, len(error_lines), result.stdout)

    def test_caps_each_call_to_time_left_before_timeout(self) -> None:
        result = self.run_script(["--check-provenance", "--timeout", "5s", TAG])
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        limits = [int(line) for line in self.calls("timeout")]
        # Two git calls resolve the tag, and each image and chart is verified once.
        self.assertEqual(2 + len(EXPECTED_REFS), len(limits), limits)
        self.assertTrue(all(limit <= 5 for limit in limits), limits)

    def test_skips_cosign_once_timeout_has_passed(self) -> None:
        # The first attestation takes the whole 2s budget, so the second is not checked.
        result = self.run_script(
            ["--check-provenance", "--timeout", "2s", TAG],
            cosign_delay=2,
        )
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        self.assertEqual(1, len(self.calls("cosign")))
        error_lines = [line for line in result.stdout.splitlines() if line.startswith("!!! Error: Timed out")]
        self.assertEqual(1, len(error_lines), result.stdout)
        self.assertIn(f"{REGISTRY}/kueueviz-backend:{TAG}@{DIGEST}", error_lines[0])
        self.assertIn("not checked before the timeout", error_lines[0])

    def test_rejects_poll_when_tag_moves_while_verifying(self) -> None:
        # The attestations of both commits are served, so each poll finds a pass for the commit it resolved.
        attestations = "\n".join([_attestation(COMMIT), _attestation(OTHER_COMMIT)])
        result = self.run_script(
            ["--check-provenance", "--timeout", "10s", TAG],
            attestation=attestations,
            tag_before=COMMIT,
            tag_after=OTHER_COMMIT,
            tag_moves_after_calls=1,
        )
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn(f'Release tag "{TAG}" moved from {COMMIT} to {OTHER_COMMIT} while verifying', result.stdout)
        self.assertEqual(1, result.stdout.count("Try again in 5 seconds..."), result.stdout)
        # Two polls: each resolves the tag, verifies every artifact, and rechecks the tag.
        self.assertEqual(2 * len(EXPECTED_REFS), len(self.calls("cosign")))
        self.assertEqual(4, len(self.calls("git")))

    def test_passes_when_one_attestation_passes_beside_a_stale_one(self) -> None:
        stale_and_current = "\n".join([_attestation(OTHER_COMMIT), _attestation(COMMIT)])
        result = self.run_script(
            ["--check-provenance", "--timeout", "10s", TAG],
            attestation=stale_and_current,
        )
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn("NOT_READY", result.stdout)
        self.assertEqual(len(EXPECTED_REFS), len(self.calls("cosign")))

    def test_fails_when_one_attestation_fails_beside_a_passing_one(self) -> None:
        passing_and_malformed = "\n".join(
            [_attestation(COMMIT), _attestation(COMMIT, build_type="https://example.com/other")]
        )
        result = self.run_script(
            ["--check-provenance", "--timeout", "10s", TAG],
            attestation=passing_and_malformed,
        )
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        self.assertIn("!!! Error: SLSA provenance check failed for", result.stdout)
        self.assertEqual(1, len(self.calls("cosign")))

    def test_timeout_names_digest_ref_and_commits_last_seen(self) -> None:
        result = self.run_script(
            ["--check-provenance", "--timeout", "3s", TAG],
            attested_commit=OTHER_COMMIT,
            tag_before=COMMIT,
        )
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        error_lines = [line for line in result.stdout.splitlines() if line.startswith("!!! Error: Timed out")]
        self.assertEqual(1, len(error_lines), result.stdout)
        error = error_lines[0]
        self.assertIn(f"{REGISTRY}/kueue:{TAG}@{DIGEST}", error)
        self.assertIn(COMMIT, error)
        self.assertIn(OTHER_COMMIT, error)

    def test_waits_while_tag_is_not_on_remote(self) -> None:
        result = self.run_script(["--check-provenance", "--timeout", "3s", TAG], tag_before="")
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        self.assertIn("could not be resolved", result.stdout)
        error_lines = [line for line in result.stdout.splitlines() if line.startswith("!!! Error: Timed out")]
        self.assertEqual(1, len(error_lines), result.stdout)
        self.assertIn(f"tag {TAG} could not be resolved in https://github.com/kubernetes-sigs/kueue", error_lines[0])
        self.assertEqual([], self.calls("cosign"))

    def test_without_flag_makes_no_provenance_calls(self) -> None:
        result = self.run_script([TAG], attested_commit=OTHER_COMMIT)
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertEqual([], self.calls("cosign"))
        self.assertEqual([], self.calls("git"))
        self.assertNotIn("SLSA", result.stdout)

    def test_rejects_check_provenance_with_prod(self) -> None:
        result = self.run_script(["--prod", "--check-provenance", TAG])
        self.assertEqual(1, result.returncode, result.stdout + result.stderr)
        self.assertIn("--check-provenance applies to staging images only", result.stdout)
        self.assertEqual([], self.calls("cosign"))


if __name__ == "__main__":
    unittest.main()
