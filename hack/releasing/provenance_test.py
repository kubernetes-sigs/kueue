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
import re
import subprocess
import sys
import tempfile
import unittest
from collections.abc import Callable
from pathlib import Path

import provenance

COMMIT = "0123456789abcdef0123456789abcdef01234567"
OTHER_COMMIT = "fedcba9876543210fedcba9876543210fedcba98"
DIGEST = "a" * 64
PROJECT = "k8s-staging-images"
PROJECT_NUMBER = "123456789012"
BUILD_ID = "build-123"
REPOSITORY = "git+https://github.com/kubernetes-sigs/kueue"


def _github_anchor(heading: str) -> str:
    """Return the GitHub anchor of a markdown heading's text."""
    text = re.sub(r"[^a-z0-9 _-]", "", heading.lower())
    return text.replace(" ", "-")


def _predicate(ref: str, commit: str) -> dict:
    return provenance.predicate(ref, commit, BUILD_ID, PROJECT, PROJECT_NUMBER)


def _statement(predicate: dict, predicate_type: str = provenance.PREDICATE_TYPE) -> dict:
    return {
        "_type": "https://in-toto.io/Statement/v1",
        "subject": [{"name": "registry.k8s.io/kueue/kueue", "digest": {"sha256": DIGEST}}],
        "predicateType": predicate_type,
        "predicate": predicate,
    }


def _b64(text: str, wrap: int | None = None, pad: bool = True) -> str:
    encoded = base64.b64encode(text.encode("utf-8")).decode("ascii")
    if not pad:
        encoded = encoded.rstrip("=")
    if wrap is None:
        return encoded
    return "\n".join(encoded[i : i + wrap] for i in range(0, len(encoded), wrap))


def _edited(edit: Callable[[dict], None]) -> dict:
    """Return a valid v0.21.0 statement after applying edit to it."""
    statement = _statement(_predicate("v0.21.0", COMMIT))
    edit(statement)
    return statement


class PredicateTest(unittest.TestCase):
    def test_source_uri(self) -> None:
        cases = [
            ("release tag", "v0.21.0", f"{REPOSITORY}@refs/tags/v0.21.0"),
            ("release candidate tag", "v0.21.0-rc.1", f"{REPOSITORY}@refs/tags/v0.21.0-rc.1"),
            ("main branch", "main", f"{REPOSITORY}@refs/heads/main"),
            ("release branch", "release-0.20", f"{REPOSITORY}@refs/heads/release-0.20"),
            ("pre-release without number is a tag", "v0.21.0-rc", f"{REPOSITORY}@refs/tags/v0.21.0-rc"),
            ("beta pre-release is a tag", "v0.21.0-beta", f"{REPOSITORY}@refs/tags/v0.21.0-beta"),
            ("beta pre-release with number is a tag", "v0.21.0-beta.1", f"{REPOSITORY}@refs/tags/v0.21.0-beta.1"),
            ("build metadata is a tag", "v0.21.0+build.1", f"{REPOSITORY}@refs/tags/v0.21.0+build.1"),
            ("leading zero is a branch", "v0.01.0", f"{REPOSITORY}@refs/heads/v0.01.0"),
            ("partial version is a branch", "v0.21", f"{REPOSITORY}@refs/heads/v0.21"),
        ]
        for name, ref, expected in cases:
            with self.subTest(name=name):
                uri = _predicate(ref, COMMIT)["buildDefinition"]["externalParameters"]["source"]["uri"]
                self.assertEqual(expected, uri)

    def test_git_commit(self) -> None:
        cases = [
            ("release tag", "v0.21.0"),
            ("release candidate tag", "v0.21.0-rc.1"),
            ("main branch", "main"),
        ]
        for name, ref in cases:
            with self.subTest(name=name):
                predicate = _predicate(ref, COMMIT)
                build_definition = predicate["buildDefinition"]
                self.assertEqual(
                    COMMIT,
                    build_definition["externalParameters"]["source"]["digest"]["gitCommit"],
                )
                self.assertEqual(
                    [{"uri": REPOSITORY, "digest": {"gitCommit": COMMIT}}],
                    build_definition["resolvedDependencies"],
                )
                self.assertEqual(
                    f"https://github.com/kubernetes-sigs/kueue/blob/{COMMIT}/"
                    "RELEASE.md#staging-artifact-provenance-v1",
                    build_definition["buildType"],
                )

    def test_builder_id(self) -> None:
        builder = _predicate("v0.21.0", COMMIT)["runDetails"]["builder"]
        self.assertEqual(
            "https://cloudbuild.googleapis.com/projects/k8s-staging-images"
            "/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com"
            "/cloudbuild.yaml",
            builder["id"],
        )

    def test_full_shape(self) -> None:
        predicate = provenance.predicate("v0.21.0", COMMIT, BUILD_ID, PROJECT, PROJECT_NUMBER)
        self.assertEqual(
            {
                "buildDefinition": {
                    "buildType": f"https://github.com/kubernetes-sigs/kueue/blob/{COMMIT}/RELEASE.md#staging-artifact-provenance-v1",
                    "externalParameters": {
                        "source": {
                            "uri": f"{REPOSITORY}@refs/tags/v0.21.0",
                            "digest": {"gitCommit": COMMIT},
                        },
                    },
                    "internalParameters": {"buildConfigSource": "cloudbuild.yaml"},
                    "resolvedDependencies": [
                        {"uri": REPOSITORY, "digest": {"gitCommit": COMMIT}},
                    ],
                },
                "runDetails": {
                    "builder": {
                        "id": "https://cloudbuild.googleapis.com/projects/k8s-staging-images"
                        "/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com"
                        "/cloudbuild.yaml",
                    },
                    "metadata": {
                        "invocationId": "https://console.cloud.google.com/cloud-build/builds/"
                        f"{BUILD_ID}?project={PROJECT_NUMBER}",
                    },
                },
            },
            predicate,
        )

    def test_build_type_fragment_is_a_release_md_anchor(self) -> None:
        release_md = Path(__file__).resolve().parents[2] / "RELEASE.md"
        path, _, anchor = provenance.BUILD_TYPE_FRAGMENT.partition("#")
        self.assertEqual("RELEASE.md", path)
        headings = []
        in_fence = False
        for line in release_md.read_text(encoding="utf-8").splitlines():
            if re.match(r"\s*(```|~~~)", line):
                in_fence = not in_fence
            elif not in_fence:
                match = re.match(r"#{1,6}\s+(.+?)\s*$", line)
                if match:
                    headings.append(match.group(1))
        anchors = {_github_anchor(heading) for heading in headings}
        self.assertIn(anchor, anchors, f"{path} has no heading with anchor #{anchor}")

    def test_invalid_input(self) -> None:
        valid = {
            "ref": "main",
            "commit": COMMIT,
            "build_id": BUILD_ID,
            "project": PROJECT,
            "project_number": PROJECT_NUMBER,
        }
        cases = [
            ("empty ref", {"ref": ""}),
            ("ref with space", {"ref": "release 0.20"}),
            ("ref with at sign", {"ref": "main@x"}),
            ("ref with double dot", {"ref": "release..x"}),
            ("ref with double slash", {"ref": "release//x"}),
            ("ref with leading dash", {"ref": "-main"}),
            ("ref with leading dot", {"ref": ".main"}),
            ("ref with leading slash", {"ref": "/main"}),
            ("ref with trailing slash", {"ref": "release-0.20/"}),
            ("ref with refs prefix", {"ref": "refs/heads/main"}),
            ("short commit", {"commit": COMMIT[:39]}),
            ("uppercase commit", {"commit": COMMIT.upper()}),
            ("non-hex commit", {"commit": "z" * 40}),
            ("empty build ID", {"build_id": ""}),
            ("empty project", {"project": ""}),
            ("project number with letters", {"project_number": "12ab"}),
        ]
        for name, override in cases:
            with self.subTest(name=name):
                args = {**valid, **override}
                with self.assertRaises(ValueError):
                    provenance.predicate(**args)


class CheckTest(unittest.TestCase):
    def test_check(self) -> None:
        tag_predicate = _predicate("v0.21.0", COMMIT)
        statement_json = json.dumps(_statement(tag_predicate))
        cases = [
            # PASS: the statement records the tag and its commit.
            ("release tag as object", _statement(tag_predicate), "v0.21.0", COMMIT, provenance.PASS),
            ("release tag as JSON text", statement_json, "v0.21.0", COMMIT, provenance.PASS),
            ("release tag as base64", _b64(statement_json), "v0.21.0", COMMIT, provenance.PASS),
            ("release tag as wrapped base64", _b64(statement_json, wrap=76), "v0.21.0", COMMIT, provenance.PASS),
            ("release tag as unpadded base64", _b64(statement_json, pad=False), "v0.21.0", COMMIT, provenance.PASS),
            ("release candidate as base64", _b64(json.dumps(_statement(_predicate("v0.21.0-rc.1", COMMIT)))), "v0.21.0-rc.1", COMMIT, provenance.PASS),
            ("beta tag as object", _statement(_predicate("v0.21.0-beta.1", COMMIT)), "v0.21.0-beta.1", COMMIT, provenance.PASS),
            ("v0.1 statement type as object", _edited(lambda s: s.update(_type="https://in-toto.io/Statement/v0.1")), "v0.21.0", COMMIT, provenance.PASS),
            ("build metadata tag as object", _statement(_predicate("v0.21.0+build.1", COMMIT)), "v0.21.0+build.1", COMMIT, provenance.PASS),
            # NOT_READY: any other ref or commit means the build has not finished yet.
            ("tag moved to another commit is not ready", _statement(_predicate("v0.21.0", OTHER_COMMIT)), "v0.21.0", COMMIT, provenance.NOT_READY),
            ("release-branch rebuild of the tagged commit is not ready", _statement(_predicate("release-0.21", COMMIT)), "v0.21.0", COMMIT, provenance.NOT_READY),
            ("main branch build is not ready", _statement(_predicate("main", COMMIT)), "v0.21.0", COMMIT, provenance.NOT_READY),
            ("another tag is not ready", _statement(_predicate("v0.20.4", COMMIT)), "v0.21.0", COMMIT, provenance.NOT_READY),
            # FAIL: malformed input, or a predicateType or buildType that is wrong.
            ("payload of unsupported type fails", 123, "v0.21.0", COMMIT, provenance.FAIL),
            ("wrong predicateType fails", _statement(tag_predicate, predicate_type="https://example.com/other"), "v0.21.0", COMMIT, provenance.FAIL),
            ("missing _type fails", _edited(lambda s: s.pop("_type")), "v0.21.0", COMMIT, provenance.FAIL),
            ("wrong _type fails", _edited(lambda s: s.update(_type="https://example.com/Statement/v1")), "v0.21.0", COMMIT, provenance.FAIL),
            ("non-string _type fails", _edited(lambda s: s.update(_type=1)), "v0.21.0", COMMIT, provenance.FAIL),
            ("deeply nested JSON fails", '{"a":' + "[" * 1000000 + "]" * 1000000 + "}", "v0.21.0", COMMIT, provenance.FAIL),
            ("buildType for another commit fails", _edited(lambda s: s["predicate"]["buildDefinition"].update(buildType=provenance.build_type(OTHER_COMMIT))), "v0.21.0", COMMIT, provenance.FAIL),
            ("missing subject fails", _edited(lambda s: s.pop("subject")), "v0.21.0", COMMIT, provenance.FAIL),
            ("missing gitCommit fails", _edited(lambda s: s["predicate"]["buildDefinition"]["externalParameters"]["source"]["digest"].pop("gitCommit")), "v0.21.0", COMMIT, provenance.FAIL),
            ("non-object subject entry fails", _edited(lambda s: s.update(subject=["sha256"])), "v0.21.0", COMMIT, provenance.FAIL),
            ("non-string digest fails", _edited(lambda s: s["subject"][0]["digest"].update(sha256=1)), "v0.21.0", COMMIT, provenance.FAIL),
            ("empty digest fails", _edited(lambda s: s["subject"][0]["digest"].update(sha256="")), "v0.21.0", COMMIT, provenance.FAIL),
            ("short digest fails", _edited(lambda s: s["subject"][0]["digest"].update(sha256=DIGEST[:63])), "v0.21.0", COMMIT, provenance.FAIL),
            ("non-hex digest fails", _edited(lambda s: s["subject"][0]["digest"].update(sha256="g" + DIGEST[1:])), "v0.21.0", COMMIT, provenance.FAIL),
            ("uppercase digest fails", _edited(lambda s: s["subject"][0]["digest"].update(sha256=DIGEST.upper())), "v0.21.0", COMMIT, provenance.FAIL),
            ("non-string source uri fails", _edited(lambda s: s["predicate"]["buildDefinition"]["externalParameters"]["source"].update(uri=1)), "v0.21.0", COMMIT, provenance.FAIL),
            ("uppercase gitCommit fails", _edited(lambda s: s["predicate"]["buildDefinition"]["externalParameters"]["source"]["digest"].update(gitCommit=COMMIT.upper())), "v0.21.0", COMMIT, provenance.FAIL),
            ("short gitCommit fails", _edited(lambda s: s["predicate"]["buildDefinition"]["externalParameters"]["source"]["digest"].update(gitCommit=COMMIT[:12])), "v0.21.0", COMMIT, provenance.FAIL),
            ("multiple subjects fail", _edited(lambda s: s["subject"].append(dict(s["subject"][0]))), "v0.21.0", COMMIT, provenance.FAIL),
            ("malformed base64 fails", "!!!not-base64", "v0.21.0", COMMIT, provenance.FAIL),
            ("base64 of invalid JSON fails", _b64("{"), "v0.21.0", COMMIT, provenance.FAIL),
            ("base64 of JSON array fails", _b64("[]"), "v0.21.0", COMMIT, provenance.FAIL),
        ]
        for name, payload, tag, commit, expected in cases:
            with self.subTest(name=name):
                result = provenance.check(payload, tag, commit)
                self.assertEqual(expected, result.status, result.reason)

    def test_reason_names_digest_ref_and_commit(self) -> None:
        payload = _statement(_predicate("main", OTHER_COMMIT))
        result = provenance.check(payload, "v0.21.0", COMMIT)
        self.assertEqual(provenance.NOT_READY, result.status)
        for expected in [f"sha256:{DIGEST}", "refs/heads/main", OTHER_COMMIT, "refs/tags/v0.21.0", COMMIT]:
            self.assertIn(expected, result.reason)

    def test_invalid_arguments(self) -> None:
        payload = _statement(_predicate("v0.21.0", COMMIT))
        cases = [
            ("branch is not a tag", "main", COMMIT),
            ("leading zero tag", "v0.01.0", COMMIT),
            ("short commit", "v0.21.0", COMMIT[:12]),
        ]
        for name, tag, commit in cases:
            with self.subTest(name=name):
                with self.assertRaises(ValueError):
                    provenance.check(payload, tag, commit)


class CliTest(unittest.TestCase):
    script = Path(__file__).with_name("provenance.py")

    def _run(self, *args: str, stdin: str = "") -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(self.script), *args],
            input=stdin,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_predicate_prints_json(self) -> None:
        result = self._run(
            "predicate",
            "--ref", "v0.21.0",
            "--commit", COMMIT,
            "--build-id", BUILD_ID,
            "--project", PROJECT,
            "--project-number", PROJECT_NUMBER,
        )
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(_predicate("v0.21.0", COMMIT), json.loads(result.stdout))

    def test_check_exit_codes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            pass_file = Path(directory) / "pass.json"
            pass_file.write_text(json.dumps(_statement(_predicate("v0.21.0", COMMIT))))
            not_ready_file = Path(directory) / "not-ready.json"
            not_ready_file.write_text(json.dumps(_statement(_predicate("main", COMMIT))))
            malformed_file = Path(directory) / "malformed.json"
            malformed_file.write_text("{")
            cases = [
                (
                    "pass",
                    ["--tag", "v0.21.0", "--commit", COMMIT, "--payload", str(pass_file)],
                    0,
                ),
                (
                    "not ready",
                    ["--tag", "v0.21.0", "--commit", COMMIT, "--payload", str(not_ready_file)],
                    2,
                ),
                (
                    "malformed payload",
                    ["--tag", "v0.21.0", "--commit", COMMIT, "--payload", str(malformed_file)],
                    1,
                ),
                (
                    "missing payload file",
                    [
                        "--tag", "v0.21.0",
                        "--commit", COMMIT,
                        "--payload", str(Path(directory) / "missing.json"),
                    ],
                    1,
                ),
                (
                    "branch passed as tag",
                    ["--tag", "main", "--commit", COMMIT, "--payload", str(pass_file)],
                    1,
                ),
                (
                    "missing required argument",
                    ["--tag", "v0.21.0", "--payload", str(pass_file)],
                    1,
                ),
            ]
            for name, args, expected in cases:
                with self.subTest(name=name):
                    result = self._run("check", *args)
                    self.assertEqual(expected, result.returncode, result.stderr)
                    self.assertNotIn("Traceback", result.stderr)

    def test_check_reads_stdin(self) -> None:
        payload = _b64(json.dumps(_statement(_predicate("v0.21.0", COMMIT))))
        result = self._run("check", "--tag", "v0.21.0", "--commit", COMMIT, stdin=payload)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertTrue(result.stdout.startswith("PASS: "), result.stdout)


if __name__ == "__main__":
    unittest.main()
