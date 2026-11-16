// Catalog and schema descriptions, agent test tasks and catalog-level tags.

package terraform

// CatalogComment is the catalog's one-line description in directories.
const CatalogComment = "HCP Terraform and Terraform Enterprise workspaces, runs, plans, policy results, state and " +
	"drift as SQL, plus offline readers for terraform.tfstate files and plan JSON. Read-only."

const catalogDocLLM = "Read-only SQL over HCP Terraform / Terraform Enterprise (API v2) and over Terraform / " +
	"OpenTofu files. Schema main reads the API with a DuckDB secret of type terraform (TOKEN, optional HOSTNAME " +
	"for Terraform Enterprise, ORGANIZATION as the default organization): organizations, projects, workspaces, " +
	"runs and their plans, applies, cost estimates, Sentinel policy checks and OPA/Sentinel policy evaluations, " +
	"state versions and outputs, workspace resources, variables and variable sets (sensitive values are always " +
	"NULL), teams and team access, agent pools and agents, private registry modules and providers, health " +
	"assessments (drift detection) and notification configurations. Hierarchy: organization -> project -> " +
	"workspace (ws-...) -> run (run-...) -> plan / apply; functions that need a parent take its id as an " +
	"argument. Schema files needs no credentials: read_state(path) and state_outputs(path) read a " +
	"terraform.tfstate (v4) file, read_plan(path) and plan_outputs(path) read `terraform show -json` plan " +
	"output; sensitive values are masked; sample:basic.tfstate and sample:change.plan.json are bundled. Schema " +
	"unified exposes runs and their plan/apply phases in the column layout shared by the Query.Farm orchestrator " +
	"workers for UNION ALL."

// CatalogDocMD is the catalog's long human-facing description.
const CatalogDocMD = "# HCP Terraform\n\n" +
	"Query [HCP Terraform](https://developer.hashicorp.com/terraform/cloud-docs) and Terraform Enterprise from " +
	"DuckDB: workspaces, runs, plans and applies, policy results, cost estimates, state versions, outputs, " +
	"resources, variables, teams, agents, the private registry and drift-detection results. Read-only: the " +
	"worker only sends GET requests, and sensitive variables and outputs are never returned.\n\n" +
	"Connect with a `terraform` secret (add `HOSTNAME` for Terraform Enterprise):\n\n" +
	"```sql\nCREATE SECRET (TYPE terraform, TOKEN '...', ORGANIZATION 'acme');\n```\n\n" +
	"Schema `files` reads `terraform.tfstate` files and `terraform show -json` plans offline, with no " +
	"credentials, for OpenTofu and Terraform alike. Schema `unified` holds runs in the shape shared by the " +
	"Query.Farm orchestrator workers."

// agentTask is one public analyst task for `vgi-lint simulate`.
type agentTask struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// agentTestTasks are graded privately by vgi-agent-tests.yaml.
var agentTestTasks = []agentTask{
	{Name: "forced_replacements", Prompt: "Using the Terraform worker's bundled sample plan sample:change.plan.json, which resources will be replaced, and which attribute forces each replacement?"},
	{Name: "plan_action_counts", Prompt: "In the bundled sample plan sample:change.plan.json, how many resource instances are planned for each action?"},
	{Name: "destroyed_resources", Prompt: "Which resource addresses does the bundled sample plan sample:change.plan.json delete without recreating them?"},
	{Name: "state_types", Prompt: "How many resource instances of each resource type are in the bundled sample state sample:basic.tfstate?"},
	{Name: "sensitive_outputs", Prompt: "Which outputs of the bundled sample state sample:basic.tfstate are sensitive?"},
	{Name: "state_dependencies", Prompt: "In the bundled sample state sample:basic.tfstate, which resource instances depend on random_pet.server?"},
	{Name: "port_values", Prompt: "What are the result values of the random_integer.port instances in the bundled sample state sample:basic.tfstate, ordered by instance index?"},
	{Name: "unknown_outputs", Prompt: "Which outputs of the bundled sample plan sample:change.plan.json will only be known after apply?"},
	{Name: "errored_runs", Prompt: "Using the HCP Terraform worker, which runs in the organization errored, and in which workspace did each run?"},
	{Name: "plan_destructions", Prompt: "Across all runs, which workspace's plans would destroy the most resources in total, and how many?"},
	{Name: "failed_applies", Prompt: "Which runs had an apply that errored?"},
	{Name: "cost_increases", Prompt: "Which runs raise the estimated monthly cost, and by how much?"},
	{Name: "soft_failed_policy", Prompt: "For run run-net-2, how many Sentinel policies passed and how many soft-failed?"},
	{Name: "opa_mandatory", Prompt: "For run run-app-2, which task stage has an OPA policy evaluation with mandatory failures, and how many?"},
	{Name: "state_history", Prompt: "How many state versions does the prod-network workspace have, and what is the highest serial?"},
	{Name: "vpc_output", Prompt: "What is the value of the vpc_id output in the current state of the prod-network workspace?"},
	{Name: "resource_types", Prompt: "How many resources of each resource type are in the current state of the prod-network workspace?"},
	{Name: "sensitive_variables", Prompt: "Which variables of the prod-network workspace are sensitive?"},
	{Name: "global_varset_keys", Prompt: "Which variable keys does the organization's global variable set define?"},
	{Name: "workspace_admins", Prompt: "Which teams have admin access to the prod-network workspace?"},
	{Name: "unhealthy_agents", Prompt: "Which self-hosted agents, in which agent pool, are neither idle nor busy?"},
	{Name: "module_versions", Prompt: "How many published versions does each private registry module have?"},
	{Name: "private_providers", Prompt: "Which providers are published in the organization's private registry?"},
	{Name: "drifted_workspaces", Prompt: "Which workspaces have drifted according to their latest health assessment?"},
	{Name: "drift_notifications", Prompt: "Which notification destinations of the prod-network workspace fire when drift is detected?"},
	{Name: "project_workspaces", Prompt: "How many workspaces does each project contain, by project name?"},
	{Name: "assessment_orgs", Prompt: "Which organizations enforce health assessments for every workspace?"},
	{Name: "unified_outcomes", Prompt: "In the cross-orchestrator unified shape, how many Terraform runs ended in each normalized status?"},
	{Name: "skipped_phases", Prompt: "In the unified task runs, which Terraform runs had a skipped apply phase?"},
}

// CatalogTags are the catalog-level discovery tags.
var CatalogTags = map[string]string{
	"vgi.title":              "HCP Terraform",
	"vgi.subtitle":           "HCP Terraform / Terraform Enterprise runs, workspaces, policies and state as SQL, plus offline tfstate and plan readers.",
	"vgi.doc_llm":            catalogDocLLM,
	"vgi.doc_md":             CatalogDocMD,
	"vgi.source_url":         SourceURL,
	"vgi.icon_url":           "https://query.farm/media-kit/products/vgi/vgi-logo.png",
	"vgi.license":            "0BSD",
	"vgi.author":             "Query.Farm",
	"vgi.copyright":          "Copyright 2026 Query Farm LLC - https://query.farm",
	"vgi.keywords":           keywordsArray("terraform, hcp terraform, terraform cloud, terraform enterprise, opentofu, infrastructure as code, tfstate, terraform plan, drift detection, sentinel, opa"),
	"vgi.support_contact":    SourceURL + "/issues",
	"vgi.support_policy_url": SourceURL + "#support",
	"vgi.agent_test_tasks":   mustJSON(agentTestTasks),
}

// schemaInfo is the per-schema discovery text.
type schemaInfo struct {
	Title, Domain, Keywords, DocLLM, DocMD string
	Categories                             []string
}

var mainSchema = schemaInfo{
	Title: "HCP Terraform API", Domain: "infrastructure as code",
	Keywords: "hcp terraform, terraform enterprise, workspaces, runs, plans, policy checks, state versions, variables, teams, agents, registry, drift",
	DocLLM: "Schema main is the live HCP Terraform / Terraform Enterprise API v2, for questions about what " +
		"infrastructure runs are doing: which runs errored or would destroy resources, what policy checks and cost " +
		"estimates said, what a workspace's current state and outputs hold, who has access, and which workspaces " +
		"have drifted. Auth: the terraform secret's TOKEN, optional HOSTNAME for Terraform Enterprise, and " +
		"ORGANIZATION as the default organization. Objects follow the API hierarchy organization -> project -> " +
		"workspace -> run -> plan / apply: organization-scoped functions accept organization := to override the " +
		"secret's default, workspace-scoped ones take the workspace positionally (its ws-... id, or its name for state history), and run-scoped ones take a " +
		"run id. Start from the workspace and run listings and join down by id. WHERE filters on run status, source, " +
		"workspace and creation time, and on workspace project, name and current-run status, push down to the API; " +
		"other predicates filter locally after paging. Read-only: only GET requests are sent, sensitive variable and " +
		"output values are always NULL, and webhook URLs are reduced to their host.",
	DocMD: "## main\n\nThe HCP Terraform / Terraform Enterprise API v2, read with the `terraform` secret's token.\n\n" +
		"Objects follow the API hierarchy: organization, project, workspace, run, then the run's plan and apply. " +
		"Functions below the organization take the parent's id as an argument.\n\n" +
		"WHERE filters on run status, source, workspace and creation time, and on workspace project, name and " +
		"current-run status, are sent to the API. Sensitive variable and output values are never returned.",
	Categories: []string{"organization", "workspaces", "runs", "state", "registry"},
}

var filesSchema = schemaInfo{
	Title: "State and Plan Files", Domain: "infrastructure as code",
	Keywords: "terraform.tfstate, state file, plan json, terraform show -json, opentofu, offline",
	DocLLM: "Schema files reads Terraform / OpenTofu documents from the worker's filesystem with no credentials: " +
		"terraform.tfstate files (state format version 4: resource instances with their attributes, and root " +
		"outputs) and the JSON printed by `terraform show -json <planfile>` (resource changes with the action, " +
		"why it was chosen, which attributes force a replacement and masked before/after values, and output " +
		"changes). Every function takes the document path as its one positional argument. Sensitive values are " +
		"masked. sample:basic.tfstate and sample:change.plan.json are bundled samples that work everywhere.",
	DocMD: "## files\n\nOffline readers for `terraform.tfstate` files and `terraform show -json` plans, from " +
		"Terraform or OpenTofu. No credentials and no network.\n\n" +
		"Sensitive values are masked. Try them on the bundled `sample:basic.tfstate` and `sample:change.plan.json`.",
	Categories: []string{"files"},
}

var unifiedSchema = schemaInfo{
	Title: "Unified Orchestrator Runs", Domain: "workflow orchestration",
	Keywords: "unified, runs, task runs, orchestrator, cross-tool, union all, terraform",
	DocLLM: "Schema unified has two tables with the exact columns the airflow, dagster, prefect, temporal, dbt, " +
		"databricks and github workers also expose, for UNION ALL across tools. runs: one row per HCP Terraform " +
		"run of the secret's ORGANIZATION (pipeline = workspace name). task_runs: the plan and apply phase of each run.",
	DocMD: "## unified\n\n`runs` and `task_runs` in the column layout shared by every Query.Farm orchestrator " +
		"worker, for `UNION ALL` across tools.\n\n" +
		"A Terraform run is a deployment of one workspace's infrastructure; its plan and apply are its task runs. " +
		"Statuses are normalized to queued, running, success, failed, cancelled and skipped.",
	Categories: []string{"unified"},
}

// Schema comments.
const (
	MainSchemaComment    = "HCP Terraform / Terraform Enterprise API: workspaces, runs, policies, state, variables, teams, agents and registry."
	FilesSchemaComment   = "Offline readers for terraform.tfstate files and terraform show -json plans."
	UnifiedSchemaComment = "runs and task_runs in the shape shared by every orchestrator worker."
)

// schemaTags builds a schema's tags.
func schemaTags(si schemaInfo, examples []exampleQuery) map[string]string {
	var cats []category
	for _, name := range si.Categories {
		for _, c := range SchemaCategories {
			if c.Name == name {
				cats = append(cats, c)
			}
		}
	}
	return map[string]string{
		"vgi.title":           si.Title,
		"domain":              si.Domain,
		"vgi.keywords":        keywordsArray(si.Keywords),
		"vgi.doc_llm":         si.DocLLM,
		"vgi.doc_md":          si.DocMD,
		"vgi.categories":      mustJSON(cats),
		"vgi.example_queries": mustJSON(examples),
	}
}
