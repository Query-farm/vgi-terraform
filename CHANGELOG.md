# Changelog

## 0.1.0 (unreleased)

Initial release.

- `terraform.main`: read-only HCP Terraform / Terraform Enterprise API v2 tables: organizations,
  projects, workspaces, runs, plans, applies, cost estimates, policy checks, policy evaluations,
  state versions, state version outputs, workspace resources, variables, variable sets, teams,
  team access, agent pools, agents, private registry modules and providers, health assessments
  and notification configurations. Filter pushdown on workspaces and runs; resumable pagination;
  429 / 5xx retries. Sensitive values are never returned.
- `terraform.files`: offline `read_state`, `state_outputs`, `read_plan`, `plan_outputs` for
  terraform.tfstate (v4) files and `terraform show -json` plans, with sensitive values masked and
  bundled OpenTofu-generated samples.
- `terraform.unified`: `runs` / `task_runs` in the shared orchestrator shape.

Generated from vgi-starter-go v1.3.0.

Targets: vgi-go 0.42 · VGI protocol 3.3 · Haybarn 2.0.0rc11 · vgi-lint-check 2.x (unpinned)
