#!/usr/bin/env python3
"""A strict, dependency-free replay of the HCP Terraform API v2 used by the tests.

The responses follow the documented JSON:API shapes (kebab-case attributes,
relationships, ``included``, ``meta.pagination``). The server is strict so the
tests catch request bugs:

* every ``/api/v2`` request needs ``Authorization: Bearer <MOCK_API_TOKEN>``
  (default ``test-token``), otherwise 401;
* every endpoint lists the query parameters it accepts; anything else
  (including an unknown ``filter[...]`` name) is a 400;
* ``page[size]`` above 100 is a 400, as on HCP Terraform.

Behaviour switches (environment):

* ``MOCK_FLAKY=1``: the first request for each distinct URL answers 429 with
  ``Retry-After: 0`` (the client's retry path is always exercised).
* ``MOCK_PAGE_LIMIT=<n>``: serve at most n items per page whatever
  ``page[size]`` asks for, so pagination is exercised on small fixtures.

Test helpers: ``GET /__log`` returns every API request seen so far as JSON
(``[{"path": ..., "url": <decoded path?query>}]``), so SQL tests can assert
which filters were pushed down; ``GET /health`` returns ``{"status": "ok"}``.

Usage::

    python3 test/mock_api/server.py            # prints PORT:<n> once listening
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qsl, unquote, urlparse

TOKEN = os.environ.get("MOCK_API_TOKEN", "test-token")
FLAKY = os.environ.get("MOCK_FLAKY") == "1"
PAGE_LIMIT = int(os.environ.get("MOCK_PAGE_LIMIT", "0") or 0)
PAGE = {"page[number]", "page[size]"}

LOCK = threading.Lock()
LOG: list[dict] = []
SEEN: set[str] = set()


def rel(typ, id_):
    return {"data": None if id_ is None else {"type": typ, "id": id_}}


def res(typ, id_, attrs, rels=None):
    return {"id": id_, "type": typ, "attributes": attrs, "relationships": rels or {}}


# ---------------------------------------------------------------- fixture data

ORGS = [
    res("organizations", "acme", {"name": "acme", "email": "ops@acme.test", "external-id": "org-ACME1",
        "plan-identifier": "standard", "default-execution-mode": "remote", "cost-estimation-enabled": True,
        "assessments-enforced": False, "saml-enabled": True, "two-factor-conformant": False,
        "created-at": "2023-01-10T09:00:00.000Z"}),
    res("organizations", "globex", {"name": "globex", "email": "infra@globex.test", "external-id": "org-GLOBEX",
        "plan-identifier": "free", "default-execution-mode": "agent", "cost-estimation-enabled": False,
        "assessments-enforced": True, "saml-enabled": False, "two-factor-conformant": True,
        "created-at": "2024-05-02T12:30:00.000Z"}),
]

PROJECTS = {"acme": [
    res("projects", "prj-core", {"name": "Core Infra", "description": "Networking and shared services",
        "workspace-count": 2, "team-count": 2, "default-execution-mode": "remote"}, {"organization": rel("organizations", "acme")}),
    res("projects", "prj-apps", {"name": "Applications", "description": None,
        "workspace-count": 2, "team-count": 1, "default-execution-mode": "agent"}, {"organization": rel("organizations", "acme")}),
]}


def ws(id_, name, project, tags, run, sv, asmt, enabled, **kw):
    attrs = {"name": name, "description": kw.get("description"), "environment": "default",
             "terraform-version": kw.get("tfv", "1.9.5"), "execution-mode": kw.get("mode", "remote"),
             "resource-count": kw.get("count", 0), "locked": kw.get("locked", False), "auto-apply": kw.get("auto", False),
             "assessments-enabled": enabled, "tag-names": tags, "working-directory": kw.get("wd", ""),
             "vcs-repo-identifier": kw.get("repo"), "source": "tfe-ui", "run-failures": kw.get("fail", 0),
             "policy-check-failures": 0, "plan-duration-average": kw.get("pda"), "apply-duration-average": kw.get("ada"),
             "latest-change-at": kw.get("changed"), "last-assessment-result-at": kw.get("asmt_at"),
             "created-at": "2024-02-01T10:00:00.000Z", "updated-at": "2026-11-01T10:00:00.000Z"}
    return res("workspaces", id_, attrs, {
        "organization": rel("organizations", "acme"), "project": rel("projects", project),
        "current-run": rel("runs", run), "current-state-version": rel("state-versions", sv),
        "current-assessment-result": rel("assessment-results", asmt)})


WORKSPACES = [
    ws("ws-net", "prod-network", "prj-core", ["prod", "network"], "run-net-2", "sv-net-2", "asmtres-net", True,
       count=42, repo="acme/network", pda=95000, ada=180500, changed="2026-11-13T09:20:00.000Z",
       asmt_at="2026-11-14T03:00:00.000Z", fail=1),
    ws("ws-app", "prod-app", "prj-apps", ["prod", "app"], "run-app-3", "sv-app-1", "asmtres-app", True,
       count=17, repo="acme/app", wd="infra", mode="agent", changed="2026-11-10T12:30:00.000Z",
       asmt_at="2026-11-14T03:05:00.000Z", fail=2),
    ws("ws-stg", "staging-app", "prj-apps", ["staging", "app"], "run-stg-1", None, None, False,
       count=9, repo="acme/app", wd="infra", auto=True, tfv="1.10.0"),
    ws("ws-sbx", "sandbox", "prj-core", [], None, None, None, True, locked=True, tfv="1.6.6"),
]
WS_BY_ID = {w["id"]: w for w in WORKSPACES}


def run(id_, wsid, status, created, source, ts, plan, apply, ce=None, **kw):
    attrs = {"status": status, "source": source, "trigger-reason": kw.get("reason", "manual"),
             "message": kw.get("message", "Triggered via UI"), "is-destroy": kw.get("destroy", False),
             "plan-only": kw.get("plan_only", False), "refresh-only": False, "has-changes": kw.get("changes", True),
             "auto-apply": False, "created-at": created, "status-timestamps": ts}
    return res("runs", id_, attrs, {
        "workspace": rel("workspaces", wsid), "plan": rel("plans", plan), "apply": rel("applies", apply),
        "cost-estimate": rel("cost-estimates", ce), "policy-checks": {"data": []}})


# Newest first, as HCP Terraform lists runs.
RUNS = [
    run("run-app-3", "ws-app", "planning", "2026-11-15T06:00:00.000Z", "tfe-api",
        {"plan-queued-at": "2026-11-15T06:00:05.000Z", "planning-at": "2026-11-15T06:00:10.000Z"},
        "plan-app-3", "apply-app-3", message="Deploy v2.4.0"),
    run("run-app-2", "ws-app", "errored", "2026-11-14T10:00:00.000Z", "tfe-configuration-version",
        {"plan-queued-at": "2026-11-14T10:00:02.000Z", "planning-at": "2026-11-14T10:00:05.000Z",
         "planned-at": "2026-11-14T10:01:05.000Z", "applying-at": "2026-11-14T10:02:00.000Z",
         "errored-at": "2026-11-14T10:04:00.000Z"},
        "plan-app-2", "apply-app-2", "ce-app-2", message="Merge pull request #91", reason="vcs"),
    run("run-net-2", "ws-net", "applied", "2026-11-13T09:00:00.000Z", "tfe-ui",
        {"plan-queued-at": "2026-11-13T09:00:01.000Z", "planning-at": "2026-11-13T09:00:03.000Z",
         "planned-at": "2026-11-13T09:01:40.000Z", "confirmed-at": "2026-11-13T09:10:00.000Z",
         "applying-at": "2026-11-13T09:10:05.000Z", "applied-at": "2026-11-13T09:20:00.000Z"},
        "plan-net-2", "apply-net-2", "ce-net-2", message="Add transit gateway"),
    run("run-stg-1", "ws-stg", "planned_and_finished", "2026-11-12T08:00:00.000Z", "tfe-api",
        {"plan-queued-at": "2026-11-12T08:00:01.000Z", "planning-at": "2026-11-12T08:00:02.000Z",
         "planned-and-finished-at": "2026-11-12T08:00:50.000Z"},
        "plan-stg-1", "apply-stg-1", changes=False, message="Nightly drift check"),
    run("run-app-1", "ws-app", "applied", "2026-11-10T12:00:00.000Z", "tfe-configuration-version",
        {"plan-queued-at": "2026-11-10T12:00:01.000Z", "planning-at": "2026-11-10T12:00:03.000Z",
         "applying-at": "2026-11-10T12:05:00.000Z", "applied-at": "2026-11-10T12:30:00.000Z"},
        "plan-app-1", "apply-app-1", "ce-app-1", message="Merge pull request #88", reason="vcs"),
    run("run-net-1", "ws-net", "discarded", "2026-11-05T15:00:00.000Z", "tfe-ui",
        {"plan-queued-at": "2026-11-05T15:00:01.000Z", "planning-at": "2026-11-05T15:00:04.000Z",
         "planned-at": "2026-11-05T15:02:00.000Z", "discarded-at": "2026-11-05T16:00:00.000Z"},
        "plan-net-1", "apply-net-1", message="Try new CIDR", destroy=False),
]
RUN_BY_ID = {r["id"]: r for r in RUNS}


def phase(typ, id_, status, ts, add, chg, dst, imp=0, changes=None):
    attrs = {"status": status, "status-timestamps": ts, "resource-additions": add, "resource-changes": chg,
             "resource-destructions": dst, "resource-imports": imp}
    if typ == "plans":
        attrs["has-changes"] = changes if changes is not None else (add + chg + dst + imp) > 0
    return res(typ, id_, attrs)


PLANS = [
    phase("plans", "plan-app-3", "running", {"queued-at": "2026-11-15T06:00:05.000Z", "started-at": "2026-11-15T06:00:10.000Z"}, 0, 0, 0),
    phase("plans", "plan-app-2", "finished", {"queued-at": "2026-11-14T10:00:02.000Z", "started-at": "2026-11-14T10:00:05.000Z", "finished-at": "2026-11-14T10:01:05.000Z"}, 2, 1, 0),
    phase("plans", "plan-net-2", "finished", {"queued-at": "2026-11-13T09:00:01.000Z", "started-at": "2026-11-13T09:00:03.000Z", "finished-at": "2026-11-13T09:01:40.000Z"}, 3, 2, 1),
    phase("plans", "plan-stg-1", "finished", {"queued-at": "2026-11-12T08:00:01.000Z", "started-at": "2026-11-12T08:00:02.000Z", "finished-at": "2026-11-12T08:00:50.000Z"}, 0, 0, 0),
    phase("plans", "plan-app-1", "finished", {"queued-at": "2026-11-10T12:00:01.000Z", "started-at": "2026-11-10T12:00:03.000Z", "finished-at": "2026-11-10T12:04:03.000Z"}, 5, 0, 4, 1),
    phase("plans", "plan-net-1", "finished", {"queued-at": "2026-11-05T15:00:01.000Z", "started-at": "2026-11-05T15:00:04.000Z", "finished-at": "2026-11-05T15:02:00.000Z"}, 0, 0, 12),
]
APPLIES = [
    phase("applies", "apply-app-3", "unreachable", {}, 0, 0, 0),
    phase("applies", "apply-app-2", "errored", {"queued-at": "2026-11-14T10:01:50.000Z", "started-at": "2026-11-14T10:02:00.000Z", "errored-at": "2026-11-14T10:04:00.000Z"}, 1, 0, 0),
    phase("applies", "apply-net-2", "finished", {"queued-at": "2026-11-13T09:10:00.000Z", "started-at": "2026-11-13T09:10:05.000Z", "finished-at": "2026-11-13T09:20:00.000Z"}, 3, 2, 1),
    phase("applies", "apply-stg-1", "unreachable", {}, 0, 0, 0),
    phase("applies", "apply-app-1", "finished", {"queued-at": "2026-11-10T12:04:30.000Z", "started-at": "2026-11-10T12:05:00.000Z", "finished-at": "2026-11-10T12:30:00.000Z"}, 5, 0, 4, 1),
    phase("applies", "apply-net-1", "unreachable", {}, 0, 0, 0),
]


def ce(id_, prior, proposed, n, matched):
    delta = f"{float(proposed) - float(prior):.2f}"
    return res("cost-estimates", id_, {"status": "finished", "prior-monthly-cost": prior,
               "proposed-monthly-cost": proposed, "delta-monthly-cost": delta, "resources-count": n,
               "matched-resources-count": matched, "unmatched-resources-count": n - matched, "error-message": None,
               "status-timestamps": {"queued-at": "2026-11-13T09:01:41.000Z", "finished-at": "2026-11-13T09:01:50.000Z"}})


COST = [ce("ce-app-2", "1200.50", "1350.75", 12, 10), ce("ce-net-2", "800.00", "1012.40", 30, 28), ce("ce-app-1", "1300.00", "1200.50", 11, 11)]
INCLUDABLE = {"plan": ("plans", "plan", PLANS), "apply": ("applies", "apply", APPLIES),
              "cost_estimate": ("cost-estimates", "cost-estimate", COST), "workspace": ("workspaces", "workspace", WORKSPACES)}

POLICY_CHECKS = {"run-net-2": [res("policy-checks", "polchk-net-2", {
    "status": "soft_failed", "scope": "organization",
    "result": {"result": False, "passed": 4, "total-failed": 1, "hard-failed": 0, "soft-failed": 1, "advisory-failed": 0, "duration-ms": 412, "sentinel": {}},
    "status-timestamps": {"queued-at": "2026-11-13T09:01:41.000Z", "soft-failed-at": "2026-11-13T09:01:45.000Z"}},
    {"run": rel("runs", "run-net-2")})]}

TASK_STAGES = {"run-app-2": [
    res("task-stages", "ts-app-2-post", {"stage": "post_plan", "status": "failed"}, {"run": rel("runs", "run-app-2")}),
    res("task-stages", "ts-app-2-pre", {"stage": "pre_apply", "status": "passed"}, {"run": rel("runs", "run-app-2")}),
]}
POLICY_EVALS = {
    "ts-app-2-post": [res("policy-evaluations", "poleval-opa-1", {
        "status": "failed", "policy-kind": "opa", "policy-tool-version": "0.61.0",
        "result-count": {"advisory-failed": 1, "errored": 0, "mandatory-failed": 2, "passed": 5},
        "status-timestamps": {"queued-at": "2026-11-14T10:01:06.000Z"},
        "created-at": "2026-11-14T10:01:06.000Z", "updated-at": "2026-11-14T10:01:20.000Z"})],
    "ts-app-2-pre": [res("policy-evaluations", "poleval-snt-1", {
        "status": "passed", "policy-kind": "sentinel", "policy-tool-version": "0.26.0",
        "result-count": {"advisory-failed": 0, "errored": 0, "mandatory-failed": 0, "passed": 3},
        "status-timestamps": {"queued-at": "2026-11-14T10:01:50.000Z"},
        "created-at": "2026-11-14T10:01:50.000Z", "updated-at": "2026-11-14T10:01:55.000Z"})],
}

STATE_VERSIONS = {("acme", "prod-network"): [
    res("state-versions", "sv-net-2", {"serial": 14, "state-version": 4, "terraform-version": "1.9.5", "status": "finalized",
        "size": 48213, "resources-processed": True, "vcs-commit-sha": "9f1c2ab", "created-at": "2026-11-13T09:20:00.000Z"},
        {"run": rel("runs", "run-net-2")}),
    res("state-versions", "sv-net-1", {"serial": 13, "state-version": 4, "terraform-version": "1.9.5", "status": "finalized",
        "size": 47002, "resources-processed": True, "vcs-commit-sha": None, "created-at": "2026-10-30T18:00:00.000Z"},
        {"run": rel("runs", None)}),
]}

OUTPUTS = [
    res("state-version-outputs", "wsout-vpc", {"name": "vpc_id", "sensitive": False, "type": "string", "value": "vpc-0a1b2c", "detailed-type": "string"}),
    res("state-version-outputs", "wsout-subnets", {"name": "subnet_ids", "sensitive": False, "type": "array",
        "value": ["subnet-1", "subnet-2"], "detailed-type": ["list", "string"]}),
    # HCP Terraform already returns null for sensitive values; the worker masks them anyway.
    res("state-version-outputs", "wsout-key", {"name": "vpn_psk", "sensitive": True, "type": "string",
        "value": "should-never-surface", "detailed-type": "string"}),
]
SV_OUTPUTS = {"sv-net-2": OUTPUTS}
WS_OUTPUTS = {"ws-net": OUTPUTS}

RESOURCES = {"ws-net": [
    res("resources", "wsr-1", {"address": "aws_vpc.main", "name": "main", "created-at": "2025-01-01T00:00:00.000Z",
        "updated-at": "2026-11-13T09:20:00.000Z", "module": "root", "provider": "hashicorp/aws",
        "provider-type": "aws_vpc", "modified-by-state-version-id": "sv-net-2", "name-index": None}),
    res("resources", "wsr-2", {"address": "aws_subnet.private[0]", "name": "private", "created-at": "2025-01-01T00:00:00.000Z",
        "updated-at": "2025-01-01T00:00:00.000Z", "module": "root", "provider": "hashicorp/aws",
        "provider-type": "aws_subnet", "modified-by-state-version-id": "sv-net-1", "name-index": "0"}),
    res("resources", "wsr-3", {"address": "aws_subnet.private[1]", "name": "private", "created-at": "2025-01-01T00:00:00.000Z",
        "updated-at": "2025-01-01T00:00:00.000Z", "module": "root", "provider": "hashicorp/aws",
        "provider-type": "aws_subnet", "modified-by-state-version-id": "sv-net-1", "name-index": "1"}),
    res("resources", "wsr-4", {"address": "module.tgw.aws_ec2_transit_gateway.this", "name": "this",
        "created-at": "2026-11-13T09:20:00.000Z", "updated-at": "2026-11-13T09:20:00.000Z", "module": "module.tgw",
        "provider": "hashicorp/aws", "provider-type": "aws_ec2_transit_gateway",
        "modified-by-state-version-id": "sv-net-2", "name-index": None}),
]}

VARS = {"ws-net": [
    res("vars", "var-region", {"key": "region", "value": "us-east-1", "description": "AWS region", "sensitive": False,
        "category": "terraform", "hcl": False, "version-id": "v1"}, {"configurable": rel("workspaces", "ws-net")}),
    res("vars", "var-cidrs", {"key": "cidrs", "value": "[\"10.0.0.0/16\"]", "description": None, "sensitive": False,
        "category": "terraform", "hcl": True, "version-id": "v2"}, {"configurable": rel("workspaces", "ws-net")}),
    # HCP Terraform returns null for sensitive values; this fixture deliberately
    # leaks one so the test proves the worker masks it regardless.
    res("vars", "var-secret", {"key": "AWS_SECRET_ACCESS_KEY", "value": "leaked-secret-value", "description": None,
        "sensitive": True, "category": "env", "hcl": False, "version-id": "v3"}, {"configurable": rel("workspaces", "ws-net")}),
]}

VARSETS = {"acme": [
    res("varsets", "varset-aws", {"name": "aws-credentials", "description": "Shared AWS credentials", "global": True,
        "priority": False, "updated-at": "2026-09-01T00:00:00.000Z", "var-count": 2, "workspace-count": 0, "project-count": 0}),
    res("varsets", "varset-tags", {"name": "default-tags", "description": None, "global": False, "priority": True,
        "updated-at": "2026-10-01T00:00:00.000Z", "var-count": 1, "workspace-count": 3, "project-count": 1}),
]}
VARSET_VARS = {"varset-aws": [
    res("vars", "var-akid", {"key": "AWS_ACCESS_KEY_ID", "value": "AKIAEXAMPLE", "sensitive": False, "category": "env",
        "hcl": False, "description": None, "created-at": "2026-09-01T00:00:00.000Z"}),
    res("vars", "var-sak", {"key": "AWS_SECRET_ACCESS_KEY", "value": None, "sensitive": True, "category": "env",
        "hcl": False, "description": None, "created-at": "2026-09-01T00:00:00.000Z"}),
]}

TEAMS = {"acme": [
    res("teams", "team-owners", {"name": "owners", "visibility": "secret", "users-count": 2, "sso-team-id": None,
        "organization-access": {"manage-workspaces": True, "manage-policies": True, "manage-providers": True}}),
    res("teams", "team-net", {"name": "network-eng", "visibility": "organization", "users-count": 5, "sso-team-id": "grp-77",
        "organization-access": {"manage-workspaces": False, "manage-policies": False, "manage-providers": False}}),
]}
TEAM_WS = {"ws-net": [
    res("team-workspaces", "tws-1", {"access": "admin", "runs": "apply", "variables": "write", "state-versions": "write",
        "sentinel-mocks": "read", "workspace-locking": True, "run-tasks": True},
        {"team": rel("teams", "team-owners"), "workspace": rel("workspaces", "ws-net")}),
    res("team-workspaces", "tws-2", {"access": "write", "runs": "apply", "variables": "read", "state-versions": "read",
        "sentinel-mocks": "none", "workspace-locking": True, "run-tasks": False},
        {"team": rel("teams", "team-net"), "workspace": rel("workspaces", "ws-net")}),
]}

POOLS = {"acme": [res("agent-pools", "apool-dc1", {"name": "datacenter-1", "organization-scoped": False, "agent-count": 3,
                  "created-at": "2025-03-01T00:00:00.000Z"})]}
AGENTS = {"apool-dc1": [
    res("agents", "agent-a", {"name": "dc1-a", "status": "idle", "ip-address": "10.1.0.10", "last-ping-at": "2026-11-15T06:59:00.000Z"}),
    res("agents", "agent-b", {"name": "dc1-b", "status": "busy", "ip-address": "10.1.0.11", "last-ping-at": "2026-11-15T06:59:30.000Z"}),
    res("agents", "agent-c", {"name": "dc1-c", "status": "errored", "ip-address": "10.1.0.12", "last-ping-at": "2026-11-02T01:00:00.000Z"}),
]}

MODULES = {"acme": [
    res("registry-modules", "mod-vpc", {"name": "vpc", "namespace": "acme", "provider": "aws", "registry-name": "private",
        "status": "setup_complete", "version-statuses": [{"version": "2.1.0", "status": "ok"}, {"version": "2.0.0", "status": "ok"}],
        "created-at": "2024-01-01T00:00:00.000Z", "updated-at": "2026-08-01T00:00:00.000Z"}),
    res("registry-modules", "mod-eks", {"name": "eks", "namespace": "acme", "provider": "aws", "registry-name": "private",
        "status": "setup_complete", "version-statuses": [{"version": "0.3.0", "status": "ok"}],
        "created-at": "2025-01-01T00:00:00.000Z", "updated-at": "2026-07-01T00:00:00.000Z"}),
]}
PROVIDERS = {"acme": [res("registry-providers", "prov-internal", {"name": "internal", "namespace": "acme",
             "registry-name": "private", "created-at": "2025-06-01T00:00:00.000Z", "updated-at": "2026-06-01T00:00:00.000Z"})]}

ASSESSMENTS = {
    "asmtres-net": res("assessment-results", "asmtres-net", {"drifted": True, "succeeded": True, "error-msg": None,
                       "created-at": "2026-11-14T03:00:00.000Z"}),
    "asmtres-app": res("assessment-results", "asmtres-app", {"drifted": False, "succeeded": False,
                       "error-msg": "provider authentication failed", "created-at": "2026-11-14T03:05:00.000Z"}),
}

NOTIFICATIONS = {"ws-net": [
    res("notification-configurations", "nc-slack", {"name": "net-alerts", "destination-type": "slack", "enabled": True,
        "token": None, "url": "https://hooks.slack.com/services/T000/B000/XXXXSECRET", "triggers": ["run:errored", "assessment:drifted"],
        "created-at": "2025-01-01T00:00:00.000Z", "updated-at": "2026-01-01T00:00:00.000Z"}),
    res("notification-configurations", "nc-email", {"name": "owners-email", "destination-type": "email", "enabled": False,
        "token": None, "url": None, "triggers": ["run:completed"],
        "created-at": "2025-01-01T00:00:00.000Z", "updated-at": "2025-01-01T00:00:00.000Z"}),
]}


# ---------------------------------------------------------------- the server

class BadRequest(Exception):
    pass


class NotFound(Exception):
    pass


def check(params, allowed, required=()):
    for k in params:
        if k not in allowed:
            raise BadRequest(f"invalid query parameter {k!r}")
    for k in required:
        if k not in params:
            raise BadRequest(f"missing required parameter {k!r}")


def paginate(items, params):
    try:
        number = int(params.get("page[number]", "1"))
        size = int(params.get("page[size]", "20"))
    except ValueError:
        raise BadRequest("page[number] and page[size] must be integers")
    if size < 1 or size > 100 or number < 1:
        raise BadRequest("page[size] must be between 1 and 100")
    if PAGE_LIMIT:
        size = min(size, PAGE_LIMIT)
    total = len(items)
    pages = max(1, (total + size - 1) // size)
    chunk = items[(number - 1) * size:number * size]
    meta = {"pagination": {"current-page": number, "page-size": size, "prev-page": number - 1 if number > 1 else None,
                           "next-page": number + 1 if number < pages else None, "total-pages": pages, "total-count": total}}
    return chunk, meta


def includes(params, allowed):
    raw = params.get("include", "")
    names = [n for n in raw.split(",") if n]
    for n in names:
        if n not in allowed:
            raise BadRequest(f"invalid include {n!r}")
    return names


def include_for(items, names):
    out, seen = [], set()
    for name in names:
        typ, relname, pool = INCLUDABLE.get(name, (None, None, None)) if name != "current_run" else ("runs", "current-run", RUNS)
        by_id = {x["id"]: x for x in pool}
        for it in items:
            data = it["relationships"].get(relname, {}).get("data")
            if data and data["id"] in by_id and (typ, data["id"]) not in seen:
                seen.add((typ, data["id"]))
                out.append(by_id[data["id"]])
    return out


def org_of(name):
    if name not in {o["id"] for o in ORGS}:
        raise NotFound()
    return name


def list_doc(items, params, allowed=(), include_allowed=None, extra_required=()):
    check(params, set(allowed) | PAGE | ({"include"} if include_allowed else set()), extra_required)
    names = includes(params, include_allowed or ())
    chunk, meta = paginate(items, params)
    doc = {"data": chunk, "meta": meta}
    if names:
        doc["included"] = include_for(chunk, names)
    return doc


def route(path, params):
    m = re.fullmatch
    if path == "/organizations":
        return list_doc(ORGS, params, {"q"})
    if g := m(r"/organizations/([^/]+)/projects", path):
        return list_doc(PROJECTS.get(org_of(g[1]), []), params, {"q", "filter[names]"})
    if g := m(r"/organizations/([^/]+)/workspaces", path):
        org_of(g[1])
        check(params, PAGE | {"include", "search[name]", "search[tags]", "search[exclude-tags]", "search[wildcard-name]",
                              "filter[project][id]", "filter[current-run][status]", "sort"})
        items = WORKSPACES if g[1] == "acme" else []
        if v := params.get("search[name]"):
            items = [w for w in items if v.lower() in w["attributes"]["name"].lower()]
        if v := params.get("search[tags]"):
            want = set(v.split(","))
            items = [w for w in items if want <= set(w["attributes"]["tag-names"])]
        if v := params.get("filter[project][id]"):
            items = [w for w in items if w["relationships"]["project"]["data"]["id"] == v]
        if v := params.get("filter[current-run][status]"):
            items = [w for w in items if (r := w["relationships"]["current-run"]["data"]) and RUN_BY_ID[r["id"]]["attributes"]["status"] == v]
        names = includes(params, {"current_run"})
        chunk, meta = paginate(items, params)
        doc = {"data": chunk, "meta": meta}
        if names:
            doc["included"] = include_for(chunk, names)
        return doc
    run_params = {"filter[status]", "filter[source]", "filter[operation]", "filter[status_group]", "filter[timeframe]",
                  "filter[agent_pool_names]", "search[user]", "search[commit]", "search[basic]"}
    run_includes = {"plan", "apply", "cost_estimate", "workspace", "created_by"}

    def runs_filtered(items):
        if v := params.get("filter[status]"):
            items = [r for r in items if r["attributes"]["status"] in v.split(",")]
        if v := params.get("filter[source]"):
            items = [r for r in items if r["attributes"]["source"] in v.split(",")]
        return items

    if g := m(r"/organizations/([^/]+)/runs", path):
        org_of(g[1])
        check(params, PAGE | {"include"} | run_params | {"filter[workspace_names]"})
        items = runs_filtered(RUNS if g[1] == "acme" else [])
        if v := params.get("filter[workspace_names]"):
            want = v.split(",")
            items = [r for r in items if WS_BY_ID[r["relationships"]["workspace"]["data"]["id"]]["attributes"]["name"] in want]
        return list_doc(items, {k: v for k, v in params.items() if k not in run_params and k != "filter[workspace_names]"}, (), run_includes)
    if g := m(r"/workspaces/([^/]+)/runs", path):
        if g[1] not in WS_BY_ID:
            raise NotFound()
        check(params, PAGE | {"include"} | run_params)
        items = runs_filtered([r for r in RUNS if r["relationships"]["workspace"]["data"]["id"] == g[1]])
        return list_doc(items, {k: v for k, v in params.items() if k not in run_params}, (), run_includes)
    if g := m(r"/runs/([^/]+)", path):
        if g[1] not in RUN_BY_ID:
            raise NotFound()
        check(params, {"include"})
        names = includes(params, run_includes)
        doc = {"data": RUN_BY_ID[g[1]]}
        if names:
            doc["included"] = include_for([RUN_BY_ID[g[1]]], names)
        return doc
    if g := m(r"/runs/([^/]+)/policy-checks", path):
        if g[1] not in RUN_BY_ID:
            raise NotFound()
        return list_doc(POLICY_CHECKS.get(g[1], []), params)
    if g := m(r"/runs/([^/]+)/task-stages", path):
        if g[1] not in RUN_BY_ID:
            raise NotFound()
        return list_doc(TASK_STAGES.get(g[1], []), params)
    if g := m(r"/task-stages/([^/]+)/policy-evaluations", path):
        if g[1] not in POLICY_EVALS:
            raise NotFound()
        return list_doc(POLICY_EVALS[g[1]], params)
    if path == "/state-versions":
        check(params, PAGE | {"filter[workspace][name]", "filter[organization][name]", "filter[status]"},
              ("filter[workspace][name]", "filter[organization][name]"))
        key = (params["filter[organization][name]"], params["filter[workspace][name]"])
        if key not in STATE_VERSIONS:
            raise NotFound()
        return list_doc(STATE_VERSIONS[key], {k: v for k, v in params.items() if k.startswith("page[")})
    if g := m(r"/state-versions/([^/]+)/outputs", path):
        if g[1] not in SV_OUTPUTS:
            raise NotFound()
        return list_doc(SV_OUTPUTS[g[1]], params)
    if g := m(r"/workspaces/([^/]+)/current-state-version-outputs", path):
        if g[1] not in WS_OUTPUTS:
            raise NotFound()
        return list_doc(WS_OUTPUTS[g[1]], params)
    if g := m(r"/workspaces/([^/]+)/resources", path):
        if g[1] not in WS_BY_ID:
            raise NotFound()
        return list_doc(RESOURCES.get(g[1], []), params)
    if g := m(r"/workspaces/([^/]+)/vars", path):
        if g[1] not in WS_BY_ID:
            raise NotFound()
        check(params, PAGE)  # documented as unpaginated; page params are tolerated
        return list_doc(VARS.get(g[1], []), params)
    if g := m(r"/organizations/([^/]+)/varsets", path):
        return list_doc(VARSETS.get(org_of(g[1]), []), params, {"q"})
    if g := m(r"/varsets/([^/]+)/relationships/vars", path):
        if g[1] not in VARSET_VARS:
            raise NotFound()
        return list_doc(VARSET_VARS[g[1]], params)
    if g := m(r"/organizations/([^/]+)/teams", path):
        return list_doc(TEAMS.get(org_of(g[1]), []), params, {"q", "filter[names]"})
    if path == "/team-workspaces":
        check(params, PAGE | {"filter[workspace][id]"}, ("filter[workspace][id]",))
        if params["filter[workspace][id]"] not in WS_BY_ID:
            raise NotFound()
        return list_doc(TEAM_WS.get(params["filter[workspace][id]"], []), {k: v for k, v in params.items() if k.startswith("page[")})
    if g := m(r"/organizations/([^/]+)/agent-pools", path):
        return list_doc(POOLS.get(org_of(g[1]), []), params, {"q", "sort"})
    if g := m(r"/agent-pools/([^/]+)/agents", path):
        if g[1] not in AGENTS:
            raise NotFound()
        return list_doc(AGENTS[g[1]], params, {"filter[last-ping-since]"})
    if g := m(r"/organizations/([^/]+)/registry-modules", path):
        return list_doc(MODULES.get(org_of(g[1]), []), params, {"q"})
    if g := m(r"/organizations/([^/]+)/registry-providers", path):
        return list_doc(PROVIDERS.get(org_of(g[1]), []), params, {"q"})
    if g := m(r"/assessment-results/([^/]+)", path):
        check(params, set())
        if g[1] not in ASSESSMENTS:
            raise NotFound()
        return {"data": ASSESSMENTS[g[1]]}
    if g := m(r"/workspaces/([^/]+)/notification-configurations", path):
        if g[1] not in WS_BY_ID:
            raise NotFound()
        return list_doc(NOTIFICATIONS.get(g[1], []), params)
    raise NotFound()


class Handler(BaseHTTPRequestHandler):
    server_version = "mock-hcp-terraform/1"

    def log_message(self, fmt, *args):  # keep stdout for PORT:<n>
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def send(self, status, body, headers=None):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/vnd.api+json")
        self.send_header("Content-Length", str(len(raw)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(raw)

    def error(self, status, title, detail=""):
        self.send(status, {"errors": [{"status": str(status), "title": title, "detail": detail}]})

    def do_GET(self):  # noqa: N802
        u = urlparse(self.path)
        if u.path == "/health":
            return self.send(200, {"status": "ok"})
        if u.path == "/__log":
            with LOCK:
                return self.send(200, list(LOG))
        if not u.path.startswith("/api/v2/"):
            return self.error(404, "not found")
        if self.headers.get("Authorization") != f"Bearer {TOKEN}":
            return self.error(401, "unauthorized")
        pairs = parse_qsl(u.query, keep_blank_values=True)
        params = dict(pairs)
        if len(params) != len(pairs):
            return self.error(400, "bad request", "repeated query parameter")
        path = u.path[len("/api/v2"):]
        with LOCK:
            LOG.append({"path": path, "url": unquote(self.path[len("/api/v2"):])})
            first = self.path not in SEEN
            SEEN.add(self.path)
        if FLAKY and first:
            return self.send(429, {"errors": [{"status": "429", "title": "Too many requests",
                             "detail": "You have exceeded the API's rate limit."}]}, {"Retry-After": "0", "X-RateLimit-Limit": "30"})
        try:
            return self.send(200, route(path, params))
        except BadRequest as e:
            return self.error(400, "bad request", str(e))
        except NotFound:
            return self.error(404, "not found")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=0)
    args = ap.parse_args()
    srv = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"PORT:{srv.server_address[1]}", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
