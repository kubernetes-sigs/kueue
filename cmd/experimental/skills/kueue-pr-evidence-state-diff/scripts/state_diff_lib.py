
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


"""Pure logic for kueue-pr-evidence-state-diff.

Scenario loading/compilation, runner selection, raw-snapshot normalization to
pr-evidence.capture/v1, base-vs-head diffing, and block rendering. No
subprocesses, no network: everything here is unit-tested offline.
"""

from __future__ import annotations

import json
import re
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

SKILL = "kueue-pr-evidence-state-diff"
SCHEMA_VERSION = "pr-evidence.capture/v1"
DRIVER_FOCUS = "pr-evidence-state-diff"

KUEUE_GROUP = "kueue.x-k8s.io"
KUEUE_KINDS = {
    "Workload", "ClusterQueue", "LocalQueue", "ResourceFlavor", "Cohort",
    "WorkloadPriorityClass", "AdmissionCheck", "Topology", "ProvisioningRequestConfig",
}
CLUSTER_SCOPED = {
    "ClusterQueue", "ResourceFlavor", "Cohort", "WorkloadPriorityClass",
    "AdmissionCheck", "Topology", "ProvisioningRequestConfig", "Node", "Namespace",
    "PriorityClass", "DeviceClass", "ResourceSlice", "MultiKueueCluster", "MultiKueueConfig",
}
CORE_V1_KINDS = {"Node", "Namespace", "Pod", "ConfigMap", "Secret", "LimitRange", "ResourceQuota"}

# Runner policy (kueue-pr-evidence-common convention 4): envtest unless the scenario needs one of these.
KIND_REQUIREMENTS = {"pods-ready", "dra", "tas-node-fit", "multikueue"}
STEP_ACTIONS = ("apply", "waitFor", "capture", "patch", "finishEviction", "finish", "delete")
KEBAB = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
VISIBLE_ROW_LIMIT = 12
BUDGET = 25  # family budget incl. the injector's header line


class ScenarioError(ValueError):
    pass


# ---------------------------------------------------------------- scenario


def parse_duration(v: Any, default: float) -> float:
    if v is None:
        return default
    if isinstance(v, (int, float)):
        return float(v)
    m = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*(ms|s|m)?\s*", str(v))
    if not m:
        raise ScenarioError(f"bad duration: {v!r} (use e.g. 10s, 500ms, 1m)")
    n, unit = float(m.group(1)), m.group(2) or "s"
    return n / 1000 if unit == "ms" else n * 60 if unit == "m" else n


def load_scenario_file(path: Path) -> dict:
    try:
        import yaml  # PyYAML
    except ImportError as e:  # pragma: no cover
        raise ScenarioError("PyYAML is required: python3 -m pip install pyyaml") from e
    data = yaml.safe_load(path.read_text())
    if not isinstance(data, dict):
        raise ScenarioError(f"{path}: scenario must be a mapping")
    for step in data.get("steps") or []:
        if isinstance(step, dict) and isinstance(step.get("apply"), str):
            step["apply"] = [d for d in yaml.safe_load_all(step["apply"]) if d]
    return data


def _ref(raw: Any, where: str, default_kind: str | None, default_ns: str | None, api: str) -> dict:
    if not isinstance(raw, dict):
        raise ScenarioError(f"{where}: expected a mapping")
    kind = raw.get("kind", default_kind)
    name = raw.get("name")
    if not kind or not name:
        raise ScenarioError(f"{where}: needs kind and name")
    ns = raw.get("namespace")
    if kind not in CLUSTER_SCOPED:
        ns = ns or default_ns
        if not ns:
            raise ScenarioError(f"{where}: namespaced {kind} needs a namespace")
    else:
        ns = ""
    if raw.get("apiVersion"):
        av = raw["apiVersion"]
    elif kind in KUEUE_KINDS:
        av = f"{KUEUE_GROUP}/{api}"
    elif kind in CORE_V1_KINDS:
        av = "v1"
    else:
        raise ScenarioError(f"{where}: apiVersion required for kind {kind}")
    return {"apiVersion": av, "kind": kind, "namespace": ns, "name": name}


def short_ref(ref: dict) -> str:
    ns = f"{ref['namespace']}/" if ref.get("namespace") else ""
    return f"{ref['kind']} {ns}{ref['name']}"


def compile_scenario(sc: dict, api: str = "v1beta2", gates: dict | None = None) -> dict:
    """Validate a loaded scenario and compile it to the driver's JSON shape.

    `gates` overrides sc["featureGates"] (used to drop gates unknown to a tree).
    """
    name = sc.get("name")
    if not isinstance(name, str) or not KEBAB.match(name):
        raise ScenarioError("scenario name must be kebab-case")
    namespaces = sc.get("namespaces") or []
    if not namespaces or not all(isinstance(n, str) and KEBAB.match(n) for n in namespaces):
        raise ScenarioError("namespaces: list at least one kebab-case namespace")
    default_ns = namespaces[0] if len(namespaces) == 1 else None
    wait_default = parse_duration(sc.get("timeout"), 10.0)
    steps_in = sc.get("steps")
    if not isinstance(steps_in, list) or not steps_in:
        raise ScenarioError("steps: must be a non-empty list")

    out_steps: list[dict] = []
    captures: list[str] = []
    pending = 0
    for i, st in enumerate(steps_in):
        where = f"steps[{i}]"
        if not isinstance(st, dict) or len(st) != 1 or next(iter(st)) not in STEP_ACTIONS:
            raise ScenarioError(f"{where}: exactly one of {', '.join(STEP_ACTIONS)}")
        action, body = next(iter(st.items()))
        if action == "apply":
            objs = body if isinstance(body, list) else [body]
            compiled = []
            for j, obj in enumerate(objs):
                if not isinstance(obj, dict) or not obj.get("apiVersion") or not obj.get("kind"):
                    raise ScenarioError(f"{where}.apply[{j}]: manifest needs apiVersion and kind")
                obj = json.loads(json.dumps(obj))
                md = obj.setdefault("metadata", {})
                if not md.get("name"):
                    raise ScenarioError(f"{where}.apply[{j}]: metadata.name required (no generateName)")
                if obj["kind"] not in CLUSTER_SCOPED and not md.get("namespace"):
                    if not default_ns:
                        raise ScenarioError(f"{where}.apply[{j}]: metadata.namespace required")
                    md["namespace"] = default_ns
                compiled.append(obj)
            out_steps.append({"apply": compiled})
        elif action == "waitFor":
            ref = _ref(body, where, None, default_ns, api)
            w: dict[str, Any] = dict(ref)
            w["timeoutSeconds"] = parse_duration(body.get("timeout"), wait_default)
            if "condition" in body and "field" in body:
                raise ScenarioError(f"{where}: use condition/status or field/equals, not both")
            if "condition" in body:
                status = str(body.get("status", "True"))
                if status not in ("True", "False", "Unknown"):
                    raise ScenarioError(f"{where}: status must be True|False|Unknown")
                w.update(condition=body["condition"], status=status)
                text = f"{short_ref(ref)} {body['condition']}={status}"
                if body.get("reason"):
                    w["reason"] = body["reason"]
                    text += f" ({body['reason']})"
            elif "field" in body:
                if "equals" not in body:
                    raise ScenarioError(f"{where}: field needs equals")
                w.update(field=body["field"], equals=body["equals"])
                text = f"{short_ref(ref)} {body['field']} == {body['equals']}"
            else:
                raise ScenarioError(f"{where}: waitFor needs condition/status or field/equals")
            w["text"] = body.get("text") or text
            out_steps.append({"waitFor": w})
            pending += 1
        elif action == "capture":
            cname = body if isinstance(body, str) else (body or {}).get("name")
            if not isinstance(cname, str) or not KEBAB.match(cname):
                raise ScenarioError(f"{where}: capture name must be kebab-case")
            if cname in captures:
                raise ScenarioError(f"{where}: duplicate capture name {cname}")
            if pending == 0:
                raise ScenarioError(f"{where}: capture {cname} has no preceding waitFor/finishEviction")
            captures.append(cname)
            pending = 0
            out_steps.append({"capture": {"name": cname}})
        elif action == "patch":
            ref = _ref(body, where, None, default_ns, api)
            if not isinstance(body.get("merge"), dict):
                raise ScenarioError(f"{where}: patch needs a merge mapping")
            sub = body.get("subresource", "")
            if sub not in ("", "status"):
                raise ScenarioError(f"{where}: subresource must be status or empty")
            out_steps.append({"patch": {**ref, "merge": body["merge"], "subresource": sub}})
        elif action in ("finishEviction", "finish"):
            ref = _ref(body, where, "Workload", default_ns, api)
            if ref["kind"] != "Workload":
                raise ScenarioError(f"{where}: {action} only applies to Workloads")
            out_steps.append({action: ref})
            if action == "finishEviction":
                pending += 1
        elif action == "delete":
            out_steps.append({"delete": _ref(body, where, None, default_ns, api)})
    if not captures:
        raise ScenarioError("scenario has no capture step")
    requires = set(sc.get("requires") or [])
    unknown = requires - KIND_REQUIREMENTS
    if unknown:
        raise ScenarioError(f"requires: unknown {sorted(unknown)}; allowed {sorted(KIND_REQUIREMENTS)}")
    return {
        "name": name,
        "namespaces": namespaces,
        "featureGates": dict(sc.get("featureGates") or {}) if gates is None else gates,
        "settleTimeoutSeconds": parse_duration(sc.get("settleTimeout"), 10.0),
        # ClusterQueue status is batched (constants.UpdatesBatchPeriod = 1s), so the
        # state must stay unchanged for longer than that before a checkpoint.
        "settleWindowSeconds": parse_duration(sc.get("settleWindow"), 2.0),
        "steps": out_steps,
    }


def capture_names(compiled: dict) -> list[str]:
    return [s["capture"]["name"] for s in compiled["steps"] if "capture" in s]


def scenario_objects(compiled: dict) -> set[tuple[str, str, str]]:
    """(kind, namespace, name) of every applied object."""
    out = set()
    for s in compiled["steps"]:
        for obj in s.get("apply", []):
            md = obj.get("metadata", {})
            out.add((obj["kind"], md.get("namespace", ""), md["name"]))
    return out


def select_runner(sc: dict, override: str = "auto") -> tuple[str, str]:
    requires = sorted(set(sc.get("requires") or []))
    pinned = sc.get("runner", "auto")
    if override != "auto":
        return override, f"forced by --runner {override}"
    if pinned not in ("auto", "envtest", "kind"):
        raise ScenarioError("runner must be auto|envtest|kind")
    if pinned != "auto":
        return pinned, f"pinned by scenario runner: {pinned}"
    if requires:
        return "kind", f"scenario requires {', '.join(requires)}"
    return "envtest", "default; scenario needs no real Pods, DRA, TAS node-fit, or MultiKueue"


# Suites differ in who starts the Kueue manager: the scheduler suite starts it in
# BeforeSuite, while tas/ and most scheduler sub-suites start it per Describe.
SETUP_FUNCS = ("managerAndSchedulerSetup", "managerSetup")
_BEFORE_SUITE_RE = re.compile(r"BeforeSuite\(func\(\)\s*\{(.*?)\n\}\)", re.S)


def manager_setup(suite_src: str, override: str | None = None) -> str | None:
    """Go expression the driver passes to fwk.StartManager, or None when the
    suite's BeforeSuite already starts the manager.

    `override` (scenario key `managerSetup`) wins; "suite" forces None.
    """
    if override:
        return None if override == "suite" else override
    m = _BEFORE_SUITE_RE.search(suite_src)
    if m and "StartManager(" in m.group(1):
        return None
    for fn in SETUP_FUNCS:
        fm = re.search(rf"^func {fn}\(([^)]*)\)\s*([^{{]*)\{{", suite_src, re.M | re.S)
        if not fm:
            continue
        params, ret = " ".join(fm.group(1).split()).rstrip(", "), fm.group(2)
        if "manager.Manager" in params:
            return fn
        variadic_only = "," not in params and params.split()[-1].startswith("...") if params else True
        if variadic_only and ("func(" in ret or "ManagerSetup" in ret):
            return f"{fn}()"
    raise ScenarioError(
        "the suite's BeforeSuite does not start the manager and no argument-free "
        f"{'/'.join(SETUP_FUNCS)} was found; set managerSetup: <Go expression> in the scenario")


# ---------------------------------------------------------------- normalization

UID_RE = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
# Go durations with a sub-minute unit (so CPU quantities like "500m" survive).
DUR_RE = re.compile(r"(?<![\w.])(?:\d+h)?(?:\d+m)?\d+(?:\.\d+)?(?:ms|µs|us|ns|s)(?![\w])")
HASH_RE = re.compile(r"-(?=[a-z0-9]{5}\b)(?=[a-z]*\d)[a-z0-9]{5}\b")
# Absolute path of the overlaid driver inside a throwaway worktree (leaks $HOME).
DRIVER_PATH_RE = re.compile(r"\S*/zz_pr_evidence_state_diff_test\.go(?::\d+)?")


def mask(msg: str | None) -> str:
    if not msg:
        return msg or ""
    msg = DRIVER_PATH_RE.sub("<driver>", msg)
    msg = UID_RE.sub("<uid>", msg)
    msg = DUR_RE.sub("<duration>", msg)
    return HASH_RE.sub("-<hash>", msg)


def expand_topology(ta: dict | None) -> dict | None:
    if not ta:
        return None
    if "domains" in ta:  # v1beta1 shape
        domains = [{"values": list(d.get("values", [])), "count": int(d.get("count", 0))} for d in ta["domains"]]
    else:
        domains = []
        for s in ta.get("slices") or []:
            pc = s.get("podCounts") or {}
            for i in range(int(s.get("domainCount", 0))):
                count = pc["universal"] if pc.get("universal") is not None else pc["individual"][i]
                vals = []
                for lv in s.get("valuesPerLevel") or []:
                    if lv.get("universal") is not None:
                        vals.append(lv["universal"])
                    else:
                        ind = lv["individual"]
                        vals.append((ind.get("prefix") or "") + ind["roots"][i] + (ind.get("suffix") or ""))
                domains.append({"values": vals, "count": int(count)})
    domains.sort(key=lambda d: d["values"])
    return {"levels": list(ta.get("levels") or []), "domains": domains}


def norm_workload(o: dict) -> dict:
    md, spec, st = o.get("metadata", {}), o.get("spec", {}), o.get("status") or {}
    conds = sorted(
        ({"type": c.get("type"), "status": c.get("status"), "reason": c.get("reason"), "message": mask(c.get("message"))}
         for c in st.get("conditions") or []),
        key=lambda c: c["type"],
    )
    adm = st.get("admission")
    admission = None
    if adm:
        psas = []
        for p in adm.get("podSetAssignments") or []:
            a = {"name": p.get("name"), "count": p.get("count"), "flavors": dict(p.get("flavors") or {}),
                 "resourceUsage": dict(p.get("resourceUsage") or {})}
            ta = expand_topology(p.get("topologyAssignment"))
            if ta:
                a["topologyAssignment"] = ta
            psas.append(a)
        admission = {"clusterQueue": adm.get("clusterQueue"), "podSetAssignments": psas}
    w = {"namespace": md.get("namespace"), "name": md.get("name"), "queueName": spec.get("queueName"),
         "priority": spec.get("priority"), "conditions": conds, "admission": admission}
    rs = st.get("requeueState")
    if rs and rs.get("count") is not None:
        w["requeueState"] = {"count": rs["count"]}
    return w


def _counts(st: dict) -> dict:
    return {k: int(st.get(k) or 0) for k in ("pendingWorkloads", "reservingWorkloads", "admittedWorkloads")}


def norm_cq(o: dict) -> dict:
    st = o.get("status") or {}
    usage = []
    for f in sorted(st.get("flavorsUsage") or [], key=lambda f: f.get("name", "")):
        res = sorted(({"name": r.get("name"), "total": str(r.get("total", "0")), "borrowed": str(r.get("borrowed", "0"))}
                      for r in f.get("resources") or []), key=lambda r: r["name"])
        usage.append({"name": f.get("name"), "resources": res})
    return {"name": o["metadata"]["name"], **_counts(st), "flavorsUsage": usage}


def norm_lq(o: dict) -> dict:
    md = o["metadata"]
    return {"namespace": md.get("namespace"), "name": md["name"],
            "clusterQueue": (o.get("spec") or {}).get("clusterQueue"), **_counts(o.get("status") or {})}


def _ts(e: dict) -> datetime:
    md = e.get("metadata") or {}
    for v in (e.get("eventTime"), e.get("firstTimestamp"), md.get("creationTimestamp")):
        if v:
            return datetime.fromisoformat(str(v).replace("Z", "+00:00"))
    return datetime.min.replace(tzinfo=timezone.utc)


class Normalizer:
    """Turns driver snapshots into capture checkpoints; keeps event order state."""

    def __init__(self, compiled: dict):
        self.namespaces = set(compiled["namespaces"])
        objs = scenario_objects(compiled)
        self.cluster_objs = {(k, n) for k, ns, n in objs if not ns}
        self.cqs = {n for k, ns, n in objs if k == "ClusterQueue"}
        self.seen: set[str] = set()
        self.order = 0

    def _in_scope(self, inv: dict) -> bool:
        if inv.get("namespace") in self.namespaces:
            return True
        return (inv.get("kind"), inv.get("name")) in self.cluster_objs

    def events(self, raw_events: list[dict]) -> list[dict]:
        fresh = []
        for e in raw_events:
            uid = (e.get("metadata") or {}).get("uid") or json.dumps(e, sort_keys=True)
            inv = e.get("involvedObject") or {}
            if uid in self.seen or not self._in_scope(inv):
                continue
            self.seen.add(uid)
            fresh.append(e)
        fresh.sort(key=lambda e: (_ts(e), (e.get("metadata") or {}).get("name", "")))
        out, keys = [], set()
        for e in fresh:
            inv = e.get("involvedObject") or {}
            ev = {"type": e.get("type"), "reason": e.get("reason"),
                  "involvedObject": {"kind": inv.get("kind"), "namespace": inv.get("namespace", ""), "name": inv.get("name")},
                  "message": mask(e.get("message") or e.get("note"))}
            key = json.dumps(ev, sort_keys=True)
            if key in keys:
                continue
            keys.add(key)
            ev["order"] = self.order
            self.order += 1
            out.append(ev)
        return out

    def checkpoint(self, index: int, name: str, assertion: str, objects: dict) -> dict:
        wls = [norm_workload(o) for o in objects.get("workloads") or []
               if (o.get("metadata") or {}).get("namespace") in self.namespaces]
        cqs = [norm_cq(o) for o in objects.get("clusterQueues") or []
               if not self.cqs or o["metadata"]["name"] in self.cqs]
        lqs = [norm_lq(o) for o in objects.get("localQueues") or []
               if (o.get("metadata") or {}).get("namespace") in self.namespaces]
        return {
            "index": index, "name": name, "assertion": assertion,
            "workloads": sorted(wls, key=lambda w: (w["namespace"], w["name"])),
            "clusterQueues": sorted(cqs, key=lambda c: c["name"]),
            "localQueues": sorted(lqs, key=lambda q: (q["namespace"], q["name"])),
            "events": self.events(objects.get("events") or []),
        }


def normalize(raw: dict, compiled: dict, run: dict) -> tuple[dict, dict | None]:
    """Raw driver output -> (capture document, normalized failure or None)."""
    n = Normalizer(compiled)
    cps = [n.checkpoint(i, c["name"], c["assertion"], c["objects"]) for i, c in enumerate(raw.get("checkpoints") or [])]
    failure = None
    f = raw.get("failure")
    if f:
        names = capture_names(compiled)
        nxt = names[len(cps)] if len(cps) < len(names) else None
        failure = {"step": f.get("step"), "action": f.get("action"), "assertion": f.get("assertion") or "",
                   "message": mask(f.get("message")), "checkpoint": nxt}
        if f.get("objects"):
            failure["observed"] = n.checkpoint(len(cps), nxt or "at-failure", f.get("assertion") or "", f["objects"])
    capture = {"schemaVersion": SCHEMA_VERSION, "run": run, "checkpoints": cps}
    return capture, failure


def dumps(doc: Any) -> str:
    return json.dumps(doc, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


# ---------------------------------------------------------------- diff

CAT_COND, CAT_ADM, CAT_REQ, CAT_PRIO = "Workload conditions", "admission", "requeueState", "priority"
CAT_MSG, CAT_CQ, CAT_USAGE, CAT_LQ, CAT_EV = "condition messages", "ClusterQueue counts", "flavorsUsage", "LocalQueues", "events"
CATEGORIES = [CAT_COND, CAT_ADM, CAT_CQ, CAT_USAGE, CAT_EV, CAT_REQ, CAT_PRIO, CAT_LQ, CAT_MSG]
RANK = {c: i for i, c in enumerate(CATEGORIES)}
ABSENT = "—"


def _adm_str(a: dict | None) -> str:
    if not a:
        return ABSENT
    parts = []
    for p in a.get("podSetAssignments") or []:
        fl = ",".join(f"{r}:{f}" for r, f in sorted((p.get("flavors") or {}).items()))
        s = f"{p.get('name')}×{p.get('count')} {fl}".strip()
        ta = p.get("topologyAssignment")
        if ta:
            s += " @" + ",".join(f"{'/'.join(d['values'])}×{d['count']}" for d in ta["domains"])
        parts.append(s)
    return f"{a.get('clusterQueue')}: " + "; ".join(parts)


def flatten(cp: dict, multi_ns: bool) -> dict[tuple[str, str], tuple[str, str]]:
    """(object, field) -> (value, category)."""
    out: dict[tuple[str, str], tuple[str, str]] = {}
    for w in cp.get("workloads", []):
        obj = f"wl {w['namespace']}/{w['name']}" if multi_ns else f"wl {w['name']}"
        for c in w.get("conditions", []):
            out[(obj, c["type"])] = (f"{c['status']} ({c['reason']})", CAT_COND)
            out[(obj, f"{c['type']}.message")] = (c.get("message") or "", CAT_MSG)
        out[(obj, "admission")] = (_adm_str(w.get("admission")), CAT_ADM)
        out[(obj, "requeueState.count")] = (str((w.get("requeueState") or {}).get("count", ABSENT)), CAT_REQ)
        out[(obj, "priority")] = (str(w.get("priority")), CAT_PRIO)
    for q in cp.get("clusterQueues", []):
        obj = f"cq {q['name']}"
        for k in ("pendingWorkloads", "reservingWorkloads", "admittedWorkloads"):
            out[(obj, k.replace("Workloads", ""))] = (str(q[k]), CAT_CQ)
        for f in q.get("flavorsUsage", []):
            for r in f.get("resources", []):
                v = r["total"] + (f" (borrowed {r['borrowed']})" if r["borrowed"] not in ("0", "") else "")
                out[(obj, f"usage {f['name']}/{r['name']}")] = (v, CAT_USAGE)
    for q in cp.get("localQueues", []):
        obj = f"lq {q['namespace']}/{q['name']}" if multi_ns else f"lq {q['name']}"
        for k in ("pendingWorkloads", "reservingWorkloads", "admittedWorkloads"):
            out[(obj, k.replace("Workloads", ""))] = (str(q[k]), CAT_LQ)
    return out


def _event_keys(cp: dict, multi_ns: bool) -> dict[str, list[str]]:
    """Messages per (object, type reason); repeated Events keep every message."""
    out: dict[str, list[str]] = {}
    for e in cp.get("events", []):
        io = e["involvedObject"]
        short = {"Workload": "wl", "ClusterQueue": "cq", "LocalQueue": "lq"}.get(io["kind"], io["kind"])
        who = f"{short} {io['namespace']}/{io['name']}" if multi_ns and io.get("namespace") else f"{short} {io['name']}"
        out.setdefault(f"{who}\x00{e['type']} {e['reason']}", []).append(e.get("message", ""))
    return out


def diff_checkpoint(base: dict | None, head: dict | None, multi_ns: bool) -> list[dict]:
    rows = []
    fb = flatten(base, multi_ns) if base else {}
    fh = flatten(head, multi_ns) if head else {}
    for key in sorted(set(fb) | set(fh)):
        vb, cb = fb.get(key, (ABSENT, None))
        vh, ch = fh.get(key, (ABSENT, None))
        if vb == vh:
            continue
        if key[1].endswith(".message") and ((key[0], key[1][: -len(".message")]) not in fb
                                            or (key[0], key[1][: -len(".message")]) not in fh):
            continue  # the condition itself appears/disappears: its row says enough
        rows.append({"object": key[0], "field": key[1], "base": vb, "head": vh, "category": cb or ch})
    eb = _event_keys(base, multi_ns) if base else {}
    eh = _event_keys(head, multi_ns) if head else {}
    for k in sorted(set(eb) | set(eh)):
        who, what = k.split("\x00")
        mb, mh = eb.get(k, []), eh.get(k, [])
        if not (mb and mh):
            n = len(mb or mh)
            label = what if n == 1 else f"{what} ×{n}"
            rows.append({"object": who, "field": "event", "base": label if mb else ABSENT,
                         "head": label if mh else ABSENT, "category": CAT_EV})
            continue
        gone = sorted((Counter(mb) - Counter(mh)).elements())
        new = sorted((Counter(mh) - Counter(mb)).elements())
        for i in range(max(len(gone), len(new))):
            rows.append({"object": who, "field": f"event {what}.message",
                         "base": gone[i] if i < len(gone) else ABSENT,
                         "head": new[i] if i < len(new) else ABSENT, "category": CAT_MSG})
    return rows


def diff_captures(base: dict, head: dict, base_failure: dict | None = None, head_failure: dict | None = None) -> dict:
    cps = base["checkpoints"] + head["checkpoints"]
    multi_ns = len({o["namespace"] for c in cps for o in c["workloads"] + c.get("localQueues", [])}) > 1
    bmap = {c["name"]: c for c in base["checkpoints"]}
    hmap = {c["name"]: c for c in head["checkpoints"]}
    names = [c["name"] for c in head["checkpoints"]] + [c["name"] for c in base["checkpoints"] if c["name"] not in hmap]
    for f in (head_failure, base_failure):
        if f and f.get("checkpoint") and f["checkpoint"] not in names:
            names.append(f["checkpoint"])
    checkpoints = []
    for name in names:
        b, h = bmap.get(name), hmap.get(name)
        note = {}
        if b is None and base_failure and base_failure.get("checkpoint") == name:
            b, note["base"] = base_failure.get("observed"), "failed"
        if h is None and head_failure and head_failure.get("checkpoint") == name:
            h, note["head"] = head_failure.get("observed"), "failed"
        # A side that never reached this checkpoint has no observation: show
        # only its assertion row instead of a column of absent values.
        rows = diff_checkpoint(b, h, multi_ns) if b is not None and h is not None else []
        checkpoints.append({"name": name, "rows": rows,
                            "base": "ok" if name in bmap else note.get("base", "not reached"),
                            "head": "ok" if name in hmap else note.get("head", "not reached")})
    changed = {r["category"] for c in checkpoints for r in c["rows"]}
    present = {CAT_COND, CAT_ADM, CAT_CQ, CAT_USAGE, CAT_EV, CAT_REQ, CAT_PRIO, CAT_MSG}
    if any(c["localQueues"] for c in base["checkpoints"] + head["checkpoints"]):
        present.add(CAT_LQ)
    return {"checkpoints": checkpoints,
            "changed": [c for c in CATEGORIES if c in changed],
            "unchanged": [c for c in CATEGORIES if c in present and c not in changed],
            "baseFailure": base_failure, "headFailure": head_failure}


# ---------------------------------------------------------------- render


def _cell(s: Any, limit: int | None = 60) -> str:
    s = str(s).replace("\n", " ").replace("|", "\\|")
    if limit and len(s) > limit:
        s = s[: limit - 1] + "…"
    if s in (ABSENT, ""):
        return s or "\"\""
    # A code span closes on a backtick run of its own length: fence with a longer one.
    tick = "`" * (max((len(r) for r in re.findall(r"`+", s)), default=0) + 1)
    pad = " " if s.startswith("`") or s.endswith("`") else ""
    return f"{tick}{pad}{s}{pad}{tick}"


def _elide_common_prefix(a: str, b: str, keep: int = 12) -> tuple[str, str]:
    """Show where two long messages start to differ (visible table only)."""
    n = 0
    while n < min(len(a), len(b)) and a[n] == b[n]:
        n += 1
    if n <= keep + 8:
        return a, b
    cut = n - keep
    return "…" + a[cut:], "…" + b[cut:]


def _rows_md(diff: dict, limit: int | None, max_rows: int | None) -> tuple[list[str], int]:
    flat = []
    for c in diff["checkpoints"]:
        for f in ("base", "head"):
            if c[f] != "ok":
                flat.append((-1, c["name"], "*assertion*", "", "✗ " + c[f] if f == "base" else "✓",
                             "✓" if f == "base" else "✗ " + c[f]))
        for r in c["rows"]:
            flat.append((RANK.get(r["category"], 99), c["name"], r["object"], r["field"], r["base"], r["head"]))
    order = {c["name"]: i for i, c in enumerate(diff["checkpoints"])}
    flat.sort(key=lambda t: (order[t[1]], t[0]))
    shown = flat
    if max_rows is not None and len(flat) > max_rows:
        keep = sorted(sorted(range(len(flat)), key=lambda i: (flat[i][0], order[flat[i][1]]))[:max_rows])
        shown = [flat[i] for i in keep]
    lines = ["| checkpoint | object | field | base | head |", "|---|---|---|---|---|"]
    prev = None
    for rank, cp, obj, field, b, h in shown:
        label = f"`{cp}`" if cp != prev else "〃"
        prev = cp
        if limit and rank == RANK[CAT_MSG] and ABSENT not in (b, h):
            b, h = _elide_common_prefix(b, h)
        bb = b if b.startswith(("✓", "✗")) else _cell(b, limit)
        hh = h if h.startswith(("✓", "✗")) else _cell(h, limit)
        lines.append(f"| {label} | {obj} | {field} | {bb} | {hh} |")
    return lines, len(flat) - len(shown)


def auto_verdict(diff: dict, n_checkpoints: int) -> str:
    bf, hf = diff.get("baseFailure"), diff.get("headFailure")
    if hf:
        return f"Head FAILS `{hf.get('assertion') or hf.get('action')}` (step {hf.get('step')}); evidence incomplete."
    changed_cps = [c for c in diff["checkpoints"] if c["rows"] or c["base"] != "ok"]
    if bf:
        lead = f"Base fails `{bf.get('assertion') or bf.get('action')}`; head passes all {n_checkpoints} checkpoints."
        return lead + (f" Changed: {', '.join(diff['changed'])}." if diff["changed"] else "")
    if not changed_cps:
        return f"No observable state change: base and head match at all {n_checkpoints} checkpoints."
    return (f"State differs at {len(changed_cps)}/{n_checkpoints} checkpoints: "
            f"{', '.join(diff['changed'])}.")


def render_block(diff: dict, meta: dict, verdict: str | None = None) -> str:
    """Block content for inject-pr-notes --file (no markers, no header)."""
    n_cp = meta["checkpoints"]
    gates = meta.get("featureGates") or {}
    gate_s = ", ".join(f"{k}={str(v).lower()}" for k, v in sorted(gates.items())) or "defaults"
    for g in meta.get("gatesUnknownAtBase") or []:
        gate_s += f" ({g} not defined at base; base ran without it)"
    runner_line = (f"Runner: {meta['runner']} (k8s {meta.get('k8sVersion') or '?'}) · gates: {gate_s} · "
                   f"scenario `{meta['scenario']}` ({n_cp} checkpoints)")
    rows, hidden = _rows_md(diff, 48, VISIBLE_ROW_LIMIT)
    total_rows = len(rows) - 2 + hidden
    out = [f"**Verdict:** {verdict or auto_verdict(diff, n_cp)}", "", runner_line, ""]
    if total_rows:
        out += rows
        if hidden:
            out.append(f"| … | | {hidden} more | | |")
        out.append("")
    unchanged = diff["unchanged"] or ["nothing else"]
    partial = any(c["base"] != "ok" or c["head"] != "ok" for c in diff["checkpoints"])
    scope = " (at the checkpoints both sides reached)" if partial else ""
    out.append(f"Unchanged{scope}: {', '.join(unchanged)}.")
    out.append(f"Repro: `{meta['repro']}`")
    out += ["", f"<details><summary>Full diff ({total_rows} rows), failures, scenario</summary>", ""]
    full, _ = _rows_md(diff, None, None)
    out += full if total_rows else ["No differences."]
    for side in ("base", "head"):
        f = diff.get(f"{side}Failure")
        if f:
            out += ["", f"**{side} failed** at step {f.get('step')} ({f.get('action')}): "
                    f"{f.get('assertion') or ''}", "", "```text", mask(f.get("message") or "").strip()[:1500], "```"]
    if meta.get("captures"):
        out += ["", f"Captures (`{SCHEMA_VERSION}`): {meta['captures']}"]
    if meta.get("scenarioYaml"):
        out += ["", "```yaml", meta["scenarioYaml"].rstrip(), "```"]
    out += ["", "</details>"]
    return "\n".join(out) + "\n"


FENCE_RE = re.compile(r"^ {0,3}(`{3,}|~{3,})(.*)$")

CODE_SPAN_RE = re.compile(r"(?<!`)(`+).*?(?<!`)\1(?!`)")


def visible_lines(content: str) -> int:
    """Same rule as inject-pr-notes: non-blank lines outside <details> bodies,
    plus every fenced line outside <details>."""
    depth = count = 0
    fence = ""  # open code fence marker; tags inside a fence are literal text
    for line in content.split("\n"):
        m = FENCE_RE.match(line)
        if fence:
            if depth == 0:
                count += 1  # GitHub renders every fenced line, blank ones too
            if m and m.group(1)[0] == fence[0] and len(m.group(1)) >= len(fence) and not m.group(2).strip():
                fence = ""
            continue
        if depth == 0 and line.strip():
            count += 1
        if m:
            fence = m.group(1)
            continue
        tags = CODE_SPAN_RE.sub("", line)  # `<details>` in a code span is text
        opens = len(re.findall(r"<details\b", tags, re.IGNORECASE))
        closes = len(re.findall(r"</details\s*>", tags, re.IGNORECASE))
        depth = max(0, depth + opens - closes)
    return count
