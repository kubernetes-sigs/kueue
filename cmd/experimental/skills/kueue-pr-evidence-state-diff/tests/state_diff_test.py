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


"""Offline tests for kueue-pr-evidence-state-diff (no cluster, no go, no network).

Run: python3 -B -m unittest discover -s cmd/experimental/skills/kueue-pr-evidence-state-diff/tests -p '*_test.py'
"""

import argparse
import contextlib
import importlib.machinery
import io
import importlib.util
import json
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SKILL_DIR = Path(__file__).resolve().parents[1]
SCRIPTS = SKILL_DIR / "scripts"
FIXTURES = Path(__file__).resolve().parent / "fixtures"
sys.path.insert(0, str(SCRIPTS))

import state_diff_lib as L  # noqa: E402


def load_cli():
    loader = importlib.machinery.SourceFileLoader("state_diff_cli", str(SCRIPTS / "state-diff"))
    spec = importlib.util.spec_from_loader("state_diff_cli", loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


def wl(name, conds=(), admission=None, ns="pr-evidence", priority=0, requeue=None):
    o = {"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload",
         "metadata": {"name": name, "namespace": ns, "uid": "0b5ba8a4-1f43-4c1c-9a0e-6b3b7f3b8f11"},
         "spec": {"queueName": "q", "priority": priority},
         "status": {"conditions": [{"type": t, "status": s, "reason": r, "message": m,
                                    "lastTransitionTime": "2026-11-01T08:00:00Z"} for t, s, r, m in conds]}}
    if admission:
        o["status"]["admission"] = admission
    if requeue is not None:
        o["status"]["requeueState"] = {"count": requeue, "requeueAt": "2026-11-01T08:00:00Z"}
    return o


def cq(name, pending=0, reserving=0, admitted=0, cpu="0"):
    return {"metadata": {"name": name},
            "status": {"pendingWorkloads": pending, "reservingWorkloads": reserving, "admittedWorkloads": admitted,
                       "flavorsUsage": [{"name": "on-demand", "resources": [{"name": "cpu", "total": cpu, "borrowed": "0"}]}]}}


def ev(uid, reason, name, ts, typ="Normal", kind="Workload", ns="pr-evidence", msg="m"):
    return {"metadata": {"uid": uid, "name": f"{name}.{uid}", "namespace": ns}, "type": typ, "reason": reason,
            "involvedObject": {"kind": kind, "namespace": ns, "name": name}, "message": msg, "eventTime": ts}


ADM = {"clusterQueue": "cq", "podSetAssignments": [{"name": "main", "count": 1, "flavors": {"cpu": "on-demand"},
                                                    "resourceUsage": {"cpu": "2"}}]}

SCENARIO = {
    "name": "demo", "namespaces": ["pr-evidence"], "featureGates": {"PriorityBoost": True},
    "steps": [
        {"apply": [{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "ClusterQueue", "metadata": {"name": "cq"}},
                   {"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "LocalQueue", "metadata": {"name": "q"},
                    "spec": {"clusterQueue": "cq"}}]},
        {"waitFor": {"kind": "ClusterQueue", "name": "cq", "condition": "Active"}},
        {"capture": "ready"},
        {"patch": {"kind": "Workload", "name": "a", "merge": {"metadata": {"annotations": {"x": "1"}}}}},
        {"waitFor": {"kind": "Workload", "name": "a", "condition": "Evicted", "reason": "Preempted"}},
        {"capture": "preempted"},
    ],
}


class CompileTest(unittest.TestCase):
    def test_compile_defaults(self):
        c = L.compile_scenario(SCENARIO)
        self.assertEqual(L.capture_names(c), ["ready", "preempted"])
        lq = c["steps"][0]["apply"][1]
        self.assertEqual(lq["metadata"]["namespace"], "pr-evidence")
        self.assertNotIn("namespace", c["steps"][0]["apply"][0]["metadata"])  # cluster scoped
        w = c["steps"][4]["waitFor"]
        self.assertEqual((w["apiVersion"], w["namespace"], w["status"], w["reason"]),
                         ("kueue.x-k8s.io/v1beta2", "pr-evidence", "True", "Preempted"))
        self.assertEqual(w["text"], "Workload pr-evidence/a Evicted=True (Preempted)")
        self.assertEqual(w["timeoutSeconds"], 10.0)

    def test_api_version_follows_tree(self):
        c = L.compile_scenario(SCENARIO, api="v1beta1")
        self.assertEqual(c["steps"][1]["waitFor"]["apiVersion"], "kueue.x-k8s.io/v1beta1")

    def test_gates_override(self):
        self.assertEqual(L.compile_scenario(SCENARIO, gates={})["featureGates"], {})

    def test_rejects(self):
        def bad(mut, pattern):
            sc = json.loads(json.dumps(SCENARIO))
            mut(sc)
            with self.assertRaisesRegex(L.ScenarioError, pattern):
                L.compile_scenario(sc)
        bad(lambda s: s.update(name="Bad_Name"), "kebab")
        bad(lambda s: s.update(namespaces=[]), "namespaces")
        bad(lambda s: s["steps"].insert(0, {"capture": "early"}), "no preceding waitFor")
        bad(lambda s: s["steps"].append({"capture": "ready"}), "duplicate|no preceding")
        bad(lambda s: s["steps"].append({"sleep": "1s"}), "exactly one of")
        bad(lambda s: s["steps"].append({"waitFor": {"kind": "Workload", "name": "a"}}), "condition/status or field")
        bad(lambda s: s["steps"].append({"waitFor": {"kind": "Workload", "name": "a", "field": "x"}}), "equals")
        bad(lambda s: s["steps"].append({"waitFor": {"kind": "Workload", "name": "a", "condition": "Admitted",
                                                     "field": "x", "equals": 1}}), "not both")
        bad(lambda s: s.update(requires=["gpus"]), "requires")
        bad(lambda s: s["steps"].append({"finish": {"kind": "ClusterQueue", "name": "cq"}}), "only applies")
        bad(lambda s: s.update(steps=[st for st in s["steps"] if "capture" not in st]), "no capture")

    def test_two_namespaces_need_explicit_namespace(self):
        sc = json.loads(json.dumps(SCENARIO))
        sc["namespaces"] = ["a", "b"]
        with self.assertRaisesRegex(L.ScenarioError, "namespace"):
            L.compile_scenario(sc)

    def test_durations(self):
        self.assertEqual(L.parse_duration("500ms", 1), 0.5)
        self.assertEqual(L.parse_duration("2m", 1), 120)
        self.assertEqual(L.parse_duration(None, 7), 7)
        with self.assertRaises(L.ScenarioError):
            L.parse_duration("soon", 1)

    def test_shipped_scenarios_compile(self):
        files = sorted((SKILL_DIR / "scenarios").glob("*.yaml"))
        self.assertGreaterEqual(len(files), 3)
        for f in files:
            with self.subTest(f.name):
                sc = L.load_scenario_file(f)
                c = L.compile_scenario(sc)
                self.assertEqual(c["name"], f.stem)
                self.assertTrue(sc.get("description"))
                self.assertEqual(L.select_runner(sc)[0], "envtest")


class RunnerTest(unittest.TestCase):
    def test_select(self):
        self.assertEqual(L.select_runner({})[0], "envtest")
        self.assertEqual(L.select_runner({"requires": ["pods-ready"]}), ("kind", "scenario requires pods-ready"))
        self.assertEqual(L.select_runner({"runner": "kind"})[0], "kind")
        self.assertEqual(L.select_runner({"requires": ["dra"]}, "envtest")[0], "envtest")
        with self.assertRaises(L.ScenarioError):
            L.select_runner({"runner": "docker"})


class SettleWindowTest(unittest.TestCase):
    def test_default_window_exceeds_cq_batch_period(self):
        sc = {"name": "s", "namespaces": ["ns"], "steps": [
            {"waitFor": {"kind": "ClusterQueue", "name": "cq", "condition": "Active"}}, {"capture": "c"}]}
        self.assertGreater(L.compile_scenario(sc)["settleWindowSeconds"], 1.0)
        self.assertEqual(L.compile_scenario({**sc, "settleWindow": "500ms"})["settleWindowSeconds"], 0.5)


class ManagerSetupTest(unittest.TestCase):
    """Who starts the Kueue manager: the suite's BeforeSuite or the driver."""

    SCHEDULER = """var _ = ginkgo.BeforeSuite(func() {
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
	fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)
})

func managerAndSchedulerSetup(ctx context.Context, mgr manager.Manager) {
}
"""
    TAS = """var _ = ginkgo.BeforeSuite(func() {
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

func managerSetup(
	resourceTransformations ...config.ResourceTransformation,
) func(ctx context.Context, mgr manager.Manager) {
}
"""

    def test_before_suite_starts_manager(self):
        self.assertIsNone(L.manager_setup(self.SCHEDULER))

    def test_variadic_setup_is_called(self):
        self.assertEqual(L.manager_setup(self.TAS), "managerSetup()")

    def test_no_arg_returning_manager_setup(self):
        src = self.TAS.replace("func managerSetup(\n\tresourceTransformations ...config.ResourceTransformation,\n) "
                               "func(ctx context.Context, mgr manager.Manager)",
                               "func managerAndSchedulerSetup() framework.ManagerSetup")
        self.assertEqual(L.manager_setup(src), "managerAndSchedulerSetup()")

    def test_setup_itself(self):
        src = self.SCHEDULER.replace("\tfwk.StartManager(ctx, cfg, managerAndSchedulerSetup)\n", "")
        self.assertEqual(L.manager_setup(src), "managerAndSchedulerSetup")

    def test_required_args_need_override(self):
        src = self.TAS.replace("resourceTransformations ...config.ResourceTransformation,",
                               "afs *config.AdmissionFairSharing, m ...[]config.DeviceClassMapping,")
        with self.assertRaisesRegex(L.ScenarioError, "managerSetup"):
            L.manager_setup(src)
        self.assertEqual(L.manager_setup(src, "managerSetup(nil)"), "managerSetup(nil)")
        self.assertIsNone(L.manager_setup(self.TAS, "suite"))


class NormalizeTest(unittest.TestCase):
    def test_mask(self):
        self.assertEqual(L.mask("uid 0b5ba8a4-1f43-4c1c-9a0e-6b3b7f3b8f11 after 1m30.5s"), "uid <uid> after <duration>")
        self.assertEqual(L.mask("insufficient quota for cpu in flavor on-demand, request > maximum capacity (500m > 0)"),
                         "insufficient quota for cpu in flavor on-demand, request > maximum capacity (500m > 0)")
        self.assertEqual(L.mask("pod job-x7k2p"), "pod job-<hash>")
        self.assertEqual(L.mask("flavor on-demand quota"), "flavor on-demand quota")  # no digit: not a hash
        self.assertEqual(L.mask(None), "")
        self.assertEqual(L.mask("failed at /home/me/.cache/kueue-pr-evidence/state-diff-worktrees/0123abcd.x/test/x/zz_pr_evidence_state_diff_test.go:244 with:"),
                         "failed at <driver> with:")

    def test_expand_topology_v1beta2(self):
        ta = {"levels": ["block", "rack"], "slices": [{
            "domainCount": 2, "podCounts": {"individual": [3, 1]},
            "valuesPerLevel": [{"universal": "b1"}, {"individual": {"prefix": "r", "roots": ["2", "1"]}}]}]}
        self.assertEqual(L.expand_topology(ta), {"levels": ["block", "rack"], "domains": [
            {"values": ["b1", "r1"], "count": 1}, {"values": ["b1", "r2"], "count": 3}]})

    def test_expand_topology_v1beta1(self):
        ta = {"levels": ["rack"], "domains": [{"values": ["r2"], "count": 1}, {"values": ["r1"], "count": 2}]}
        self.assertEqual([d["values"] for d in L.expand_topology(ta)["domains"]], [["r1"], ["r2"]])

    def test_normalize_capture_and_events(self):
        compiled = L.compile_scenario(SCENARIO)
        raw = {"checkpoints": [
            {"name": "ready", "assertion": "a1", "objects": {
                "workloads": [wl("b", [("QuotaReserved", "False", "Pending", "x")]),
                              wl("a", [("Admitted", "True", "Admitted", "ok")], ADM, requeue=1),
                              wl("other", ns="elsewhere")],
                "clusterQueues": [cq("cq", 1, 1, 1, "2"), cq("foreign")],
                "localQueues": [{"metadata": {"name": "q", "namespace": "pr-evidence"}, "spec": {"clusterQueue": "cq"},
                                 "status": {"pendingWorkloads": 1}}],
                "events": [ev("2", "Admitted", "a", "2026-11-01T08:00:02Z"),
                           ev("1", "QuotaReserved", "a", "2026-11-01T08:00:01Z"),
                           ev("3", "QuotaReserved", "a", "2026-11-01T08:00:03Z"),  # same content -> deduped
                           ev("4", "Foo", "x", "2026-11-01T08:00:00Z", ns="kube-system"),
                           ev("5", "Active", "cq", "2026-11-01T08:00:00Z", kind="ClusterQueue", ns="")]}},
            {"name": "preempted", "assertion": "a2", "objects": {
                "workloads": [], "clusterQueues": [], "localQueues": [],
                "events": [ev("1", "QuotaReserved", "a", "2026-11-01T08:00:01Z"),  # seen -> dropped
                           ev("6", "Preempted", "a", "2026-11-01T08:00:05Z", typ="Warning")]}}]}
        run = {"skill": L.SKILL, "side": "base"}
        cap, failure = L.normalize(raw, compiled, run)
        self.assertIsNone(failure)
        self.assertEqual(cap["schemaVersion"], "pr-evidence.capture/v1")
        c0, c1 = cap["checkpoints"]
        self.assertEqual([w["name"] for w in c0["workloads"]], ["a", "b"])
        a = c0["workloads"][0]
        self.assertEqual(a["requeueState"], {"count": 1})
        self.assertEqual(a["admission"]["podSetAssignments"][0]["flavors"], {"cpu": "on-demand"})
        self.assertNotIn("lastTransitionTime", a["conditions"][0])
        self.assertEqual([q["name"] for q in c0["clusterQueues"]], ["cq"])
        self.assertEqual(c0["localQueues"][0]["pendingWorkloads"], 1)
        self.assertEqual([(e["reason"], e["order"]) for e in c0["events"]],
                         [("Active", 0), ("QuotaReserved", 1), ("Admitted", 2)])
        self.assertEqual([(e["reason"], e["order"]) for e in c1["events"]], [("Preempted", 3)])
        golden = json.loads((FIXTURES / "normalize.golden.json").read_text())
        self.assertEqual(json.loads(L.dumps(cap)), golden)

    def test_failure_observed_snapshot(self):
        compiled = L.compile_scenario(SCENARIO)
        raw = {"checkpoints": [{"name": "ready", "assertion": "a1", "objects": {}}],
               "failure": {"step": 4, "action": "waitFor", "assertion": "Workload pr-evidence/a Evicted=True",
                           "message": "timed out after 10.001s", "objects": {"workloads": [wl("a")]}}}
        _, f = L.normalize(raw, compiled, {})
        self.assertEqual(f["checkpoint"], "preempted")
        self.assertEqual(f["message"], "timed out after <duration>")
        self.assertEqual(f["observed"]["workloads"][0]["name"], "a")


def capture(*cps):
    return {"schemaVersion": L.SCHEMA_VERSION, "run": {}, "checkpoints": [
        {"index": i, "name": n, "assertion": "", "workloads": w, "clusterQueues": q, "localQueues": [], "events": e}
        for i, (n, w, q, e) in enumerate(cps)]}


def nwl(name, conds, admission=None):
    return L.norm_workload(wl(name, conds, admission))


def nev(reason, name, order, typ="Normal"):
    return {"type": typ, "reason": reason, "involvedObject": {"kind": "Workload", "namespace": "pr-evidence", "name": name},
            "message": "m", "order": order}


class DiffRenderTest(unittest.TestCase):
    def setUp(self):
        adm_wl = nwl("a", [("Admitted", "True", "Admitted", "")], ADM)
        self.base = capture(("ready", [adm_wl], [L.norm_cq(cq("cq", 0, 1, 1, "2"))], []))
        self.head = capture(
            ("ready", [adm_wl], [L.norm_cq(cq("cq", 0, 1, 1, "2"))], []),
            ("preempted", [nwl("a", [("Admitted", "False", "NoReservation", ""), ("Evicted", "True", "Preempted", "")])],
             [L.norm_cq(cq("cq", 1, 0, 0, "0"))], [nev("Preempted", "a", 0, "Warning")]))
        self.meta = {"scenario": "demo", "runner": "envtest", "k8sVersion": "1.37.0", "checkpoints": 2,
                     "featureGates": {"PriorityBoost": True}, "gatesUnknownAtBase": [],
                     "repro": "state-diff run --scenario demo --commit abc", "captures": "`x`", "scenarioYaml": "name: demo"}

    def test_identical(self):
        d = L.diff_captures(self.base, self.base)
        self.assertEqual(d["changed"], [])
        self.assertIn("No observable state change", L.auto_verdict(d, 1))

    def test_base_failure_is_diffed(self):
        observed = capture(("x", [nwl("a", [("Admitted", "True", "Admitted", "")], ADM)],
                            [L.norm_cq(cq("cq", 0, 1, 1, "2"))], []))["checkpoints"][0]
        bf = {"step": 4, "action": "waitFor", "assertion": "Workload pr-evidence/a Evicted=True (Preempted)",
              "message": "timed out", "checkpoint": "preempted", "observed": observed}
        d = L.diff_captures(self.base, self.head, base_failure=bf)
        cp = d["checkpoints"][1]
        self.assertEqual((cp["name"], cp["base"], cp["head"]), ("preempted", "failed", "ok"))
        fields = {(r["object"], r["field"]): (r["base"], r["head"]) for r in cp["rows"]}
        self.assertEqual(fields[("wl a", "Admitted")], ("True (Admitted)", "False (NoReservation)"))
        self.assertEqual(fields[("wl a", "Evicted")], ("—", "True (Preempted)"))
        self.assertEqual(fields[("cq cq", "usage on-demand/cpu")], ("2", "0"))
        self.assertEqual(fields[("wl a", "event")], ("—", "Warning Preempted"))
        self.assertEqual(d["changed"][:4], ["Workload conditions", "admission", "ClusterQueue counts", "flavorsUsage"])
        self.assertIn("events", d["changed"])
        self.assertNotIn("events", d["unchanged"])

        block = L.render_block(d, self.meta)
        self.assertTrue(block.startswith("**Verdict:** Base fails `Workload pr-evidence/a Evicted=True (Preempted)`"))
        self.assertIn("Runner: envtest (k8s 1.37.0) · gates: PriorityBoost=true", block)
        self.assertIn("| `preempted` | *assertion* |  | ✗ failed | ✓ |", block)
        self.assertIn("Unchanged (at the checkpoints both sides reached): requeueState, priority", block)
        self.assertNotIn("Evicted.message", block)  # condition appears: its row says enough
        self.assertIn("Repro: `state-diff run --scenario demo --commit abc`", block)
        self.assertLessEqual(L.visible_lines(block) + 1, L.BUDGET)
        self.assertNotRegex(block, r"pr-evidence:[a-z0-9-]+:(start|end)")
        self.assertNotIn("####", block)

    def test_not_reached_checkpoint_is_one_row(self):
        bf = {"step": 2, "action": "waitFor", "assertion": "x", "checkpoint": "ready", "observed": None}
        d = L.diff_captures(capture(), self.head, base_failure=bf)
        self.assertEqual([(c["name"], c["base"], c["rows"]) for c in d["checkpoints"]],
                         [("ready", "failed", []), ("preempted", "not reached", [])])

    def test_message_rows_elide_common_prefix(self):
        long = "couldn't assign flavors to pod set main: insufficient unused quota, 1 more needed"
        b = capture(("c", [nwl("a", [("QuotaReserved", "False", "Pending", long)])], [], []))
        h = capture(("c", [nwl("a", [("QuotaReserved", "False", "Pending", long + ". Pending preemption")])], [], []))
        block = L.render_block(L.diff_captures(b, h), {**self.meta, "checkpoints": 1})
        visible, details = block.split("<details>", 1)
        self.assertIn("| `… more needed` | `… more needed. Pending preemption` |", visible)
        self.assertIn(long + ". Pending preemption", details)

    def test_head_failure_verdict(self):
        hf = {"step": 2, "action": "waitFor", "assertion": "boom", "checkpoint": "ready"}
        d = L.diff_captures(self.base, capture(), head_failure=hf)
        self.assertTrue(L.auto_verdict(d, 2).startswith("Head FAILS `boom`"))

    def test_budget_caps_rows(self):
        many = [nwl(f"w{i}", [("QuotaReserved", "False", "Pending", "")]) for i in range(30)]
        many_h = [nwl(f"w{i}", [("QuotaReserved", "True", "QuotaReserved", "")]) for i in range(30)]
        d = L.diff_captures(capture(("c", many, [], [])), capture(("c", many_h, [], [])))
        block = L.render_block(d, {**self.meta, "checkpoints": 1})
        self.assertLessEqual(L.visible_lines(block) + 1, L.BUDGET)
        self.assertIn("more | | |", block)
        details = block.split("<details>", 1)[1]
        self.assertEqual(details.count("| QuotaReserved |"), 30)

    def test_gates_unknown_at_base_noted(self):
        d = L.diff_captures(self.base, self.base)
        block = L.render_block(d, {**self.meta, "gatesUnknownAtBase": ["PriorityBoost"]})
        self.assertIn("PriorityBoost not defined at base", block)

    def test_visible_lines_matches_injector(self):
        common = SKILL_DIR.parent / "kueue-pr-evidence-common" / "scripts" / "inject-pr-notes"
        loader = importlib.machinery.SourceFileLoader("inject_pr_notes", str(common))
        spec = importlib.util.spec_from_loader("inject_pr_notes", loader)
        inj = importlib.util.module_from_spec(spec)
        loader.exec_module(inj)
        block = L.render_block(L.diff_captures(self.base, self.head), self.meta)
        self.assertEqual(inj.visible_lines(block), L.visible_lines(block))

    def test_visible_lines_counts_fenced_lines(self):
        fenced = "v\n```\n<details>\n" + "x\n" * 30 + "</details>\n```\n<details>\nhidden\n```\n</details>\n```\n</details>"
        self.assertEqual(L.visible_lines(fenced), 1 + 1 + 1 + 30 + 1 + 1 + 1)

    def test_repeated_events_compare_as_multisets(self):
        def with_events(*msgs):
            e = [{**nev("Pending", "a", i), "message": m} for i, m in enumerate(msgs)]
            return capture(("x", [], [], e))
        rows = L.diff_captures(with_events("first", "same"), with_events("same"))["checkpoints"][0]["rows"]
        self.assertEqual([(r["field"], r["base"], r["head"]) for r in rows],
                         [("event Normal Pending.message", "first", L.ABSENT)])
        rows = L.diff_captures(with_events("x", "y"), with_events("x", "y"))["checkpoints"][0]["rows"]
        self.assertEqual(rows, [])
        rows = L.diff_captures(with_events(), with_events("x", "y"))["checkpoints"][0]["rows"]
        self.assertEqual([(r["field"], r["base"], r["head"]) for r in rows], [("event", L.ABSENT, "Normal Pending ×2")])

    def test_local_queue_namespaces_select_qualified_names(self):
        def with_lqs(pending):
            lqs = [{"namespace": ns, "name": "q", "pendingWorkloads": p, "reservingWorkloads": 0, "admittedWorkloads": 0}
                   for ns, p in zip(("ns-a", "ns-b"), pending)]
            return {"schemaVersion": L.SCHEMA_VERSION, "run": {}, "checkpoints": [
                {"index": 0, "name": "x", "assertion": "", "workloads": [], "clusterQueues": [], "localQueues": lqs, "events": []}]}
        rows = L.diff_captures(with_lqs((0, 0)), with_lqs((0, 1)))["checkpoints"][0]["rows"]
        self.assertEqual([(r["object"], r["field"]) for r in rows], [("lq ns-b/q", "pending")])

    def test_visible_lines_ignores_details_in_code_spans(self):
        self.assertEqual(L.visible_lines("use `<details>` here\na\nb"), 3)
        self.assertEqual(L.visible_lines("``a `<details>` b``\nc"), 2)

    def test_cell_fences_backticks(self):
        self.assertEqual(L._cell("plain"), "`plain`")
        self.assertEqual(L._cell("a `b` c"), "``a `b` c``")
        self.assertEqual(L._cell("`x``"), "``` `x`` ```")


class CliTest(unittest.TestCase):
    def test_render_offline_from_fixture_dir(self):
        with tempfile.TemporaryDirectory() as t:
            out = Path(t) / "block.md"
            sdir = Path(t) / "state-diff" / "priority-boost-no-wpc"
            shutil.copytree(FIXTURES / "run-priority-boost", sdir)
            p = subprocess.run([sys.executable, "-B", str(SCRIPTS / "state-diff"), "render",
                                "--dir", str(sdir), "--out-file", str(out)],
                               capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stderr)
            block = out.read_text()
            self.assertTrue(block.startswith("**Verdict:**"))
            self.assertLessEqual(L.visible_lines(block) + 1, L.BUDGET)

    def test_render_builds_run_json_from_captures(self):
        with tempfile.TemporaryDirectory() as t:
            sdir = Path(t) / "state-diff" / "priority-boost-no-wpc"
            shutil.copytree(FIXTURES / "run-priority-boost", sdir)
            (sdir / "run.json").unlink()
            p = subprocess.run([sys.executable, "-B", str(SCRIPTS / "state-diff"), "render", "--dir", str(sdir)],
                               capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stderr)
            meta = json.loads((sdir / "run.json").read_text())
            head = json.loads((sdir / "capture.head.json").read_text())
            self.assertEqual(meta["headSha"], head["run"]["sha"])
            self.assertEqual(meta["checkpoints"], len(head["checkpoints"]))
            self.assertTrue((Path(t) / f"{L.SKILL}.md").read_text().startswith("**Verdict:**"))

    def render_dir(self, t, mutate):
        sdir = Path(t) / "state-diff" / "priority-boost-no-wpc"
        shutil.copytree(FIXTURES / "run-priority-boost", sdir)
        mutate(sdir)
        p = subprocess.run([sys.executable, "-B", str(SCRIPTS / "state-diff"), "render", "--dir", str(sdir)],
                           capture_output=True, text=True)
        return sdir, p

    def test_render_rebuilds_stale_run_json(self):
        def bump(sdir):
            cap = json.loads((sdir / "capture.head.json").read_text())
            cap["run"]["k8sVersion"] = "1.38.0"
            (sdir / "capture.head.json").write_text(json.dumps(cap))
        with tempfile.TemporaryDirectory() as t:
            sdir, p = self.render_dir(t, bump)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertIn("rebuilding it", p.stderr)
            self.assertEqual(json.loads((sdir / "run.json").read_text())["k8sVersion"], "1.38.0")

    def test_render_rejects_mismatched_captures(self):
        def rename(sdir):
            cap = json.loads((sdir / "capture.base.json").read_text())
            cap["run"]["scenario"] = "other"
            (sdir / "capture.base.json").write_text(json.dumps(cap))
        with tempfile.TemporaryDirectory() as t:
            _, p = self.render_dir(t, rename)
            self.assertEqual(p.returncode, 2)
            self.assertIn("different scenarios", p.stderr)

    def test_head_failure_block_is_not_publishable(self):
        def fail_head(sdir):
            meta = json.loads((sdir / "run.json").read_text())
            meta["headFailure"] = {"step": 3, "action": "waitFor", "assertion": "x", "message": "timed out",
                                   "checkpoint": None, "observed": None}
            (sdir / "run.json").write_text(json.dumps(meta))
            (Path(t) / f"{L.SKILL}.md").write_text("old block")
        with tempfile.TemporaryDirectory() as t:
            sdir, p = self.render_dir(t, fail_head)
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertFalse((Path(t) / f"{L.SKILL}.md").exists())
            self.assertTrue((sdir / "block.head-failed.md").exists())

    def test_commit_excludes_base_and_head(self):
        cli = load_cli()
        a = argparse.Namespace(commit="abc", base="def", head=None)
        with self.assertRaises(SystemExit) as cm, contextlib.redirect_stderr(io.StringIO()) as err:
            cli.resolve_shas(a, Path("."))
        self.assertEqual(cm.exception.code, 2)
        self.assertIn("--commit cannot be combined", err.getvalue())

    def test_driver_template_placeholders(self):
        cli = load_cli()
        tmpl = (SCRIPTS / "driver" / "driver_test.go.tmpl").read_text()
        self.assertEqual(set(re.findall(r"\{\{[A-Z_]+\}\}", tmpl)),
                         {"{{PACKAGE}}", "{{KUEUE_API_VERSION}}", "{{EVICT_HELPER_PKG}}", "{{FINISH_HELPER_PKG}}",
                          "{{MANAGER_HOOKS}}"})
        self.assertIn('ginkgo.Describe("pr-evidence-state-diff"', tmpl)
        info = {"package": "tas", "api": "v1beta2", "evictPkg": "e", "finishPkg": "f", "managerSetup": "managerSetup()"}
        go = cli.render_driver(info)
        self.assertNotIn("{{", go)
        self.assertIn("fwk.StartManager(ctx, cfg, managerSetup())", go)
        self.assertIn("fwk.StopManager(ctx)", go)
        go = cli.render_driver({**info, "managerSetup": None})
        self.assertNotIn("StartManager", go)
        self.assertNotRegex(tmpl, r"time\.Sleep")
        self.assertEqual(cli.DRIVER_FILE, "zz_pr_evidence_state_diff_test.go")

    def test_scripts_never_edit_pr_or_write_markers(self):
        for f in SCRIPTS.rglob("*"):
            if f.is_file():
                text = f.read_text(errors="ignore")
                with self.subTest(f.name):
                    self.assertNotIn("gh pr edit", text)
                    self.assertNotRegex(text, r"pr-evidence:[a-z0-9-]+:(start|end)")


if __name__ == "__main__":
    unittest.main()
