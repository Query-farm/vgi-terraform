<p align="center">
  <img src="https://query.farm/media-kit/products/vgi/vgi-logo.png" alt="Vector Gateway Interface (VGI)" width="320">
</p>

# Query HCP Terraform Runs, Workspaces, State and Plans in DuckDB

> **vgi-terraform** · a [Query.Farm](https://query.farm) VGI worker

[![CI](https://github.com/Query-farm/vgi-terraform/actions/workflows/ci.yml/badge.svg)](https://github.com/Query-farm/vgi-terraform/actions/workflows/ci.yml)
[![vgi worker: query.farm](https://query.farm/media-kit/shields/vgi-worker.svg)](https://query.farm)

[HCP Terraform](https://developer.hashicorp.com/terraform/cloud-docs) (formerly Terraform Cloud)
and Terraform Enterprise hold every workspace, run, plan, apply, policy result, cost estimate,
state version and drift-detection result of your infrastructure behind a JSON:API. This worker
turns them into SQL tables in DuckDB, so "which workspaces errored this week", "which runs
destroy the most resources", "which teams have admin on production" or "what drifted" are a
`GROUP BY` away.

It also reads **Terraform and OpenTofu files offline**, with no credentials: `terraform.tfstate`
files and the JSON of `terraform show -json plan.bin` become rows, including **which attribute
forces each replacement**.

- **Read-only.** The worker only sends `GET` requests.
- **Secrets never leak.** Sensitive variables, variable-set variables and state outputs always
  come back `NULL`; sensitive state attributes and plan values are replaced by
  `"(sensitive value)"`; notification webhooks are reduced to their host.
- **Pushdown.** `WHERE` filters on run status, source, workspace and creation time, and on a
  workspace's project, name and current-run status, become HCP Terraform API filters.
- **Unified shape.** `unified.runs` / `unified.task_runs` have the same columns as the Airflow,
  Dagster, Prefect, Temporal, dbt, Databricks and GitHub workers, so Terraform deployments
  `UNION ALL` with the rest of your orchestration.

## Quick start

```sql
INSTALL vgi FROM community;     -- Haybarn 2.0; on DuckDB 2.0 use the query_farm repository
LOAD vgi;
ATTACH 'terraform' (TYPE vgi, LOCATION 'github-auto://Query-farm/vgi-terraform@v0.1.1');

-- No credentials needed for files: the bundled sample plan
SELECT address, action, action_reason, replace_paths
FROM terraform.files.read_plan('sample:change.plan.json')
WHERE action IN ('replace', 'delete')
ORDER BY address;

-- HCP Terraform (add HOSTNAME 'tfe.example.com' for Terraform Enterprise)
CREATE SECRET (TYPE terraform, TOKEN '...', ORGANIZATION 'acme');

SELECT workspace_name, count(*) AS errored
FROM terraform.main.runs()
WHERE status = 'errored' AND created_at >= TIMESTAMPTZ '2026-11-01 00:00:00+00'
GROUP BY ALL ORDER BY errored DESC;
```

The secret type is registered by the worker, so run `CREATE SECRET` after `ATTACH`.

<details><summary>Other ways to run it</summary>

- **DuckDB 2.0**:
  `SET allow_extension_repositories = 'allowed'; CREATE EXTENSION REPOSITORY IF NOT EXISTS query_farm WITH PREFIX 'https://duckdb-extensions.query.farm'; INSTALL vgi FROM query_farm; LOAD vgi FROM query_farm;`
  then attach as above.
- **Docker** (HTTP): `docker run -p 8000:8000 ghcr.io/query-farm/vgi-terraform:0.1.1`, then
  `ATTACH 'terraform' (TYPE vgi, LOCATION 'http://localhost:8000')`.
- **From source**: `make build` (Go 1.26+ and a C toolchain, for cgo), then
  `ATTACH 'terraform' (TYPE vgi, LOCATION '/abs/path/to/bin/vgi-terraform-worker')`.

v0.1.0 is not published yet. Until it is, build from source.
</details>

## Secrets and configuration

```sql
CREATE SECRET (TYPE terraform, TOKEN '<user, team or organization token>', ORGANIZATION 'acme');
CREATE SECRET (TYPE terraform, TOKEN '...', HOSTNAME 'tfe.example.com', ORGANIZATION 'acme');
```

| field | meaning |
|---|---|
| `TOKEN` | API token (user, team or organization token), sent as `Authorization: Bearer`. Required. |
| `HOSTNAME` | API host; default `app.terraform.io`. Set it for Terraform Enterprise. |
| `ORGANIZATION` | Default organization for the `organization :=` argument and the `unified` tables. |
| `BASE_URL` | Full base URL overriding `HOSTNAME` (e.g. a TFE on a custom port or a local mirror); `/api/v2` is appended. |

The token is read on every call and never stored in scan state, logs or error messages. Only
schema `main` and `unified` need a secret; schema `files` works without one.

## What's inside

### `terraform.main`: the HCP Terraform / Terraform Enterprise API

| object | arguments | one row per |
|---|---|---|
| `organizations` (table) | | organization the token can see |
| `projects()` | `organization :=` | project |
| `workspaces()` | `organization :=`, `tags :=` | workspace, with current-run status |
| `runs()` | `workspace_id :=` or `organization :=` | run, newest first, with plan resource counts |
| `plans()` / `applies()` / `cost_estimates()` | same as `runs` | the run's plan / apply / cost estimate |
| `policy_checks(run_id)` | | Sentinel policy check |
| `policy_evaluations(run_id)` | | OPA / Sentinel policy evaluation, per task stage |
| `state_versions(workspace_name)` | `organization :=` | state version (metadata only) |
| `state_version_outputs()` | `state_version_id :=` or `workspace_id :=` | output (sensitive values `NULL`) |
| `workspace_resources(workspace_id)` | | resource in the current state |
| `variables(workspace_id)` | | workspace variable (sensitive values `NULL`) |
| `variable_sets()` / `varset_variables(varset_id)` | `organization :=` | variable set / its variables |
| `teams()` / `team_access(workspace_id)` | `organization :=` | team / team grant on a workspace |
| `agent_pools()` / `agents(agent_pool_id)` | `organization :=` | agent pool / agent |
| `registry_modules()` / `registry_providers()` | `organization :=` | private registry module / provider |
| `assessments()` | `organization :=` | workspace's current drift-detection result |
| `notification_configurations(workspace_id)` | | notification destination (host only, no secrets) |

### `terraform.files`: offline readers (no credentials)

| function | reads | one row per |
|---|---|---|
| `read_state(path)` | `terraform.tfstate` (format v4: Terraform 0.12+, OpenTofu) | resource instance: address, type, index key, provider, attributes (JSON, sensitive masked), dependencies, tainted |
| `state_outputs(path)` | `terraform.tfstate` | root output (sensitive values `NULL`) |
| `read_plan(path)` | `terraform show -json plan.bin` / `tofu show -json plan.bin` | resource change: action, raw actions, action reason, `replace_paths`, changed attributes, masked before/after, `after_unknown` |
| `plan_outputs(path)` | the same plan JSON | output change |

`path` is a file on the worker's filesystem, or `sample:basic.tfstate` /
`sample:change.plan.json`, two documents bundled in the binary (generated with OpenTofu and the
`null` / `random` / `local` providers by `scripts/gen-terraform-fixtures.sh`).

```sql
-- Which attribute forces each replacement?
SELECT address, replace_paths[1] AS forced_by
FROM terraform.files.read_plan('sample:change.plan.json')
WHERE action = 'replace' ORDER BY address;

-- An inventory of a state file
SELECT type, count(*) FROM terraform.files.read_state('/path/to/terraform.tfstate') GROUP BY type;
```

### `terraform.unified`: cross-orchestrator runs

`unified.runs` has one row per run of the secret's organization (`pipeline` = workspace name,
`status` normalized to queued / running / success / failed / cancelled, `raw_status` the HCP
Terraform status, `trigger` = manual / api / event). `unified.task_runs` has the run's `plan` and
`apply` phases (an apply that never ran is `skipped`).

## Pushdown

| table | pushed to the API |
|---|---|
| `workspaces()` | `project_id =` → `filter[project][id]`, `name =` → `search[name]`, `current_run_status =` → `filter[current-run][status]`; `tags :=` → `search[tags]` |
| `runs()` | `status =` / `IN` → `filter[status]`, `source =` / `IN` → `filter[source]`, `workspace_name =` / `IN` → `filter[workspace_names]`, `created_at >=` / `>` stops paging once older runs appear |
| `plans()` / `applies()` / `cost_estimates()` | `run_created_at >=` / `>` stops paging early |
| `unified.runs` | `status` / `raw_status` / `pipeline` (= or `IN`) → `filter[status]` / `filter[workspace_names]`; `queued_at >=` stops paging |
| `unified.task_runs` | `run_id =` / `IN` reads just those runs |

Every pushed predicate is still applied exactly by the worker, so a fuzzy API filter
(`search[name]`) or one the API ignores never changes the result. `LIKE` and other expressions
are evaluated in the worker. Pagination (`page[number]`, `page[size]=100`) lives in the
resumable scan state, so long scans survive the HTTP transport; 429 and 5xx responses are
retried, honouring `Retry-After` / `X-RateLimit-Reset` (HCP Terraform allows 30 requests per
second, and 30 per minute on the run listings).

## Known limitations

- **TIMESTAMPTZ filters in a non-UTC session** (vgi-go 0.42.0): with ICU loaded and `TimeZone`
  set to anything but UTC, a pushed `created_at >= TIMESTAMPTZ '...'` fails with "comparison
  operands have incompatible types". Run `SET TimeZone = 'UTC'` first.
- **Expressions over JSON and TIMESTAMPTZ columns** (`LIKE`, functions) in `WHERE` on
  `workspaces()`, `runs()` and the unified tables are evaluated over the column's text, because
  the SDK's embedded evaluator has neither type. Plain comparisons and `IN` are exact.
- The API omits speculative (plan-only) runs from workspace run listings.
- `assessments()` reads one assessment result per workspace that has one.
- State and plan readers parse the whole document in memory (up to 1 GiB).

## Testing

```bash
make test-unit                 # Go unit tests (masking, pushdown, status mapping, client retries)
make test-sql                  # test/sql/*.test through Haybarn (stdio)
make test-sql TRANSPORT=http   # …over HTTP (also: launch)
make check                     # everything CI runs
```

The API tests run against `test/mock_api/server.py`, a strict replay of the documented HCP
Terraform JSON:API shapes: it requires the bearer token, rejects unknown query parameters
(including `filter[...]` names) and `page[size]` above 100, answers the first request of every
URL with a 429, serves two items per page, and logs every request at `/__log` so the SQL tests
assert which filters were pushed down. The state and plan fixtures are real OpenTofu output;
regenerate them with `scripts/gen-terraform-fixtures.sh /path/to/tofu` (only the `null`,
`random` and `local` providers, no cloud account).

## Support

Open an issue on this repository. For VGI itself, see [query.farm](https://query.farm).

## Authorship & License

Written by [Query.Farm](https://query.farm) and released under the
[BSD Zero Clause License](LICENSE) (0BSD).

Copyright 2026 Query Farm LLC - https://query.farm
