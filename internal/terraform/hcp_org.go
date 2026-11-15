// Organization-level tables: organizations, projects, workspaces, teams,
// team access, agent pools, agents, variables, variable sets, notification
// configurations, health assessments and the private registry.

package terraform

import (
	"context"
	"net/url"
	"strings"

	"github.com/Query-farm/vgi-go/vgi"
)

// ex is a (non-executable) catalog example.
func ex(description, sql string) vgi.CatalogExample {
	return vgi.CatalogExample{Description: description, SQL: sql}
}

type orgArgs struct {
	Organization *string `vgi:"name=organization,doc=Organization name; default the ORGANIZATION of the terraform secret."`
}

type workspaceIDArgs struct {
	WorkspaceID string `vgi:"name=workspace_id,pos=0,doc=Workspace id (ws-...); list them with terraform.main.workspaces()."`
}

// orgStart queues /organizations/:org/<suffix>.
func orgStart(suffix string, extra url.Values) func(context.Context, *client, *vgi.ProcessParams, *ScanState) error {
	return func(_ context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error {
		var a orgArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		org, err := c.cfg.org(a.Organization)
		if err != nil {
			return err
		}
		q := url.Values{}
		for k, v := range extra {
			q[k] = v
		}
		single(st, "/organizations/"+url.PathEscape(org)+"/"+suffix, q, map[string]string{"organization": org})
		return nil
	}
}

// workspaceStart queues /workspaces/:workspace_id/<suffix> (or a path built by mk).
func workspaceStart(mk func(id string) (string, url.Values)) func(context.Context, *client, *vgi.ProcessParams, *ScanState) error {
	return func(_ context.Context, _ *client, p *vgi.ProcessParams, st *ScanState) error {
		var a workspaceIDArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		id := strings.TrimSpace(a.WorkspaceID)
		if id == "" {
			return errEmptyArg("workspace_id")
		}
		path, q := mk(id)
		single(st, path, q, map[string]string{"workspace_id": id})
		return nil
	}
}

type argError string

func (e argError) Error() string { return string(e) }

func errEmptyArg(name string) error { return argError(name + " must not be empty") }

// ---- organizations ----------------------------------------------------------

var organizationsDef = &hcpDef{
	Name: "organizations",
	Docs: fnDocs{
		Title: "HCP Terraform Organizations", Category: "organization",
		Keywords:    "hcp terraform, terraform cloud, terraform enterprise, organizations, orgs",
		Description: "Organizations the token can see, with plan, execution-mode and security settings.",
		DocLLM: "One row per HCP Terraform / Terraform Enterprise organization visible to the secret's token. Use it to " +
			"find organization names for the organization := argument of the other tables, or to audit settings " +
			"(default execution mode, cost estimation, SAML, 2FA conformance, drift assessments enforced). No arguments.",
		DocMD: "## organizations\n\nOrganizations visible to the token: name, email, plan, default execution mode, " +
			"cost estimation, assessments enforcement, SAML and two-factor conformance.",
		Examples: []vgi.CatalogExample{
			ex("Organizations and their default execution mode", "SELECT name, default_execution_mode, cost_estimation_enabled FROM terraform.main.organizations ORDER BY name"),
		},
	},
	Cols: []colDef{
		cID("name", "Organization name (its id)."),
		cStr("email", "email", "Notification email of the organization."),
		cStr("external_id", "external-id", "Immutable external id (org-...)."),
		cStr("plan_identifier", "plan-identifier", "Subscription plan, e.g. free, standard, plus."),
		cStr("default_execution_mode", "default-execution-mode", "Default execution mode for new workspaces: remote, local or agent."),
		cBool("cost_estimation_enabled", "cost-estimation-enabled", "Whether cost estimation runs on plans."),
		cBool("assessments_enforced", "assessments-enforced", "Whether health assessments (drift detection) are enforced for every workspace."),
		cBool("saml_enabled", "saml-enabled", "Whether SAML single sign-on is enabled."),
		cBool("two_factor_conformant", "two-factor-conformant", "Whether every member has two-factor authentication enabled."),
		cTS("created_at", "created-at", "When the organization was created."),
	},
	Start: func(_ context.Context, _ *client, _ *vgi.ProcessParams, st *ScanState) error {
		single(st, "/organizations", url.Values{}, nil)
		return nil
	},
}

// ---- projects ---------------------------------------------------------------

var projectsDef = &hcpDef{
	Name: "projects",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Projects", Category: "organization",
		Keywords:    "projects, workspace grouping, hcp terraform projects",
		Description: "Projects of an organization with their workspace and team counts.",
		DocLLM: "One row per project in an organization (organization := argument, default the secret's ORGANIZATION). " +
			"Projects group workspaces; join project id to terraform.main.workspaces().project_id.",
		DocMD: "## projects(organization := ...)\n\nProjects with description, workspace count, team count and default execution mode.",
		Examples: []vgi.CatalogExample{
			ex("Projects by workspace count", "SELECT name, workspace_count FROM terraform.main.projects() ORDER BY workspace_count DESC"),
		},
	},
	Cols: []colDef{
		cID("id", "Project id (prj-...)."),
		cStr("name", "name", "Project name."),
		cStr("description", "description", "Project description."),
		cInt("workspace_count", "workspace-count", "Number of workspaces in the project."),
		cInt("team_count", "team-count", "Number of teams with access to the project."),
		cStr("default_execution_mode", "default-execution-mode", "Default execution mode of new workspaces in the project."),
		cParent("organization", "organization", "Organization name.", false),
	},
	Start: orgStart("projects", nil),
}

// ---- workspaces -------------------------------------------------------------

type workspacesArgs struct {
	Organization *string `vgi:"name=organization,doc=Organization name; default the ORGANIZATION of the terraform secret."`
	Tags         *string `vgi:"name=tags,doc=Comma-separated tag names a workspace must all carry (sent as search[tags])."`
}

func workspaceCurrentRun(r *resource, x *rowCtx) *resource {
	return x.doc.included("runs", r.relID("current-run"))
}

var workspacesDef = &hcpDef{
	Name:   "workspaces",
	Args:   workspacesArgs{},
	Filter: true,
	Docs: fnDocs{
		Title: "Workspaces", Category: "workspaces",
		Keywords:    "workspaces, terraform workspaces, current run, resource count, tags, project, drift",
		Description: "Workspaces of an organization: project, Terraform version, execution mode, resource count, tags and current run status.",
		DocLLM: "One row per workspace in an organization (organization := argument, default the secret's ORGANIZATION). " +
			"WHERE filters push down to the API: project_id = '...' (filter[project][id]), name = '...' (search[name]) " +
			"and current_run_status = '...' (filter[current-run][status]); the tags := 'a,b' argument requires all " +
			"listed tags. Every predicate is still applied exactly. Workspace ids (ws-...) feed workspace_id arguments " +
			"of runs, variables, workspace_resources, team_access and notification_configurations. resource_count is " +
			"the number of resources in the current state; current_run_status is the latest run's status (applied, " +
			"errored, planned, ...).",
		DocMD: "## workspaces(organization := ..., tags := ...)\n\nWorkspaces with project, Terraform version, execution " +
			"mode, VCS repo, resource count, tags, lock state and the current run's status.\n\n**Pushdown:** `project_id =`, " +
			"`name =` and `current_run_status =` become API filters.",
		Examples: []vgi.CatalogExample{
			ex("Workspaces whose latest run errored", "SELECT name, terraform_version, current_run_id FROM terraform.main.workspaces() WHERE current_run_status = 'errored' ORDER BY name"),
			ex("Resources under management per project", "SELECT project_id, count(*) AS workspaces, sum(resource_count) AS resources FROM terraform.main.workspaces() GROUP BY project_id ORDER BY resources DESC"),
		},
	},
	Cols: []colDef{
		cID("id", "Workspace id (ws-...)."),
		cStr("name", "name", "Workspace name."),
		cRel("organization", "organization", "Organization name."),
		cRel("project_id", "project", "Project id (prj-...)."),
		cStr("description", "description", "Workspace description."),
		cStr("terraform_version", "terraform-version", "Terraform version (or constraint) the workspace runs."),
		cStr("execution_mode", "execution-mode", "remote, local or agent."),
		cInt("resource_count", "resource-count", "Resources in the current state."),
		cBool("locked", "locked", "Whether the workspace is locked."),
		cBool("auto_apply", "auto-apply", "Whether successful plans apply automatically."),
		cBool("assessments_enabled", "assessments-enabled", "Whether health assessments (drift detection) run."),
		cList("tag_names", "tag-names", "Workspace tags."),
		cStr("working_directory", "working-directory", "Directory Terraform runs in, relative to the configuration root."),
		cStr("vcs_repo_identifier", "vcs-repo-identifier", "VCS repository (owner/repo) the workspace is connected to."),
		cStr("source", "source", "How the workspace was created, e.g. tfe-ui, tfe-api, terraform."),
		cRel("current_run_id", "current-run", "Id of the latest run (run-...)."),
		{column("current_run_status", tString, "Status of the latest run: applied, errored, planned, planned_and_finished, canceled, ...", true),
			func(r *resource, x *rowCtx) any { return workspaceCurrentRun(r, x).str("status") }},
		cRel("current_state_version_id", "current-state-version", "Id of the current state version (sv-...)."),
		cRel("current_assessment_result_id", "current-assessment-result", "Id of the latest health assessment result."),
		cInt("run_failures", "run-failures", "Number of failed runs."),
		cInt("policy_check_failures", "policy-check-failures", "Number of failed policy checks."),
		cSecondsFromMillis("plan_duration_average_seconds", "plan-duration-average", "Average plan duration in seconds."),
		cSecondsFromMillis("apply_duration_average_seconds", "apply-duration-average", "Average apply duration in seconds."),
		cTS("latest_change_at", "latest-change-at", "When the workspace's state last changed."),
		cTS("last_assessment_result_at", "last-assessment-result-at", "When the last health assessment finished."),
		cTS("created_at", "created-at", "When the workspace was created."),
		cTS("updated_at", "updated-at", "When the workspace settings last changed."),
		{column("url", tString, "Link to the workspace in the HCP Terraform / TFE UI.", true),
			func(r *resource, x *rowCtx) any {
				name, _ := r.str("name").(string)
				return x.c.cfg.workspaceURL(r.relID("organization"), name)
			}},
	},
	Start: func(_ context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error {
		var a workspacesArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		org, err := c.cfg.org(a.Organization)
		if err != nil {
			return err
		}
		q := url.Values{"include": {"current_run"}}
		if a.Tags != nil && strings.TrimSpace(*a.Tags) != "" {
			q.Set("search[tags]", strings.TrimSpace(*a.Tags))
		}
		pf := p.CurrentPushdownFilters
		if v, ok := eqStrings(pf, "project_id"); ok && len(v) == 1 {
			q.Set("filter[project][id]", v[0])
		}
		if v, ok := eqStrings(pf, "name"); ok && len(v) == 1 {
			q.Set("search[name]", v[0])
		}
		if v, ok := eqStrings(pf, "current_run_status"); ok && len(v) == 1 {
			q.Set("filter[current-run][status]", v[0])
		}
		single(st, "/organizations/"+url.PathEscape(org)+"/workspaces", q, map[string]string{"organization": org})
		return nil
	},
}

// ---- variables --------------------------------------------------------------

// maskedValue returns a variable's value, or NULL when it is sensitive.
func maskedValue(r *resource, _ *rowCtx) any {
	if s, _ := r.boolean("sensitive").(bool); s {
		return nil
	}
	if v, ok := r.attr("value").(string); ok {
		return v
	}
	return nil
}

var variablesDef = &hcpDef{
	Name: "variables",
	Args: workspaceIDArgs{},
	Docs: fnDocs{
		Title: "Workspace Variables", Category: "workspaces",
		Keywords:    "variables, workspace variables, environment variables, terraform variables, sensitive",
		Description: "Terraform and environment variables set on a workspace; sensitive values are always NULL.",
		DocLLM: "One row per variable of one workspace (workspace_id positional). category is 'terraform' (an input " +
			"variable) or 'env' (an environment variable for the run). value is NULL whenever sensitive is true: the " +
			"API never returns those values and the worker masks them regardless. Variables inherited from variable " +
			"sets are not listed here; see variable_sets / varset_variables.",
		DocMD: "## variables(workspace_id)\n\nWorkspace variables: key, value (NULL when sensitive), category, HCL flag, description.",
		Examples: []vgi.CatalogExample{
			ex("Non-sensitive Terraform variables of a workspace", "SELECT key, value FROM terraform.main.variables('ws-abc123') WHERE category = 'terraform' AND NOT sensitive ORDER BY key"),
		},
	},
	Cols: []colDef{
		cID("id", "Variable id (var-...)."),
		cParent("workspace_id", "workspace_id", "Workspace id.", false),
		cStr("key", "key", "Variable name."),
		{column("value", tString, "Value; NULL when sensitive.", true), maskedValue},
		cBool("sensitive", "sensitive", "Whether the value is write-only (never returned)."),
		cStr("category", "category", "'terraform' (input variable) or 'env' (environment variable)."),
		cBool("hcl", "hcl", "Whether the value is parsed as HCL."),
		cStr("description", "description", "Variable description."),
		cStr("version_id", "version-id", "Version id of the variable's current value."),
	},
	Start: workspaceStart(func(id string) (string, url.Values) {
		return "/workspaces/" + url.PathEscape(id) + "/vars", url.Values{}
	}),
}

var variableSetsDef = &hcpDef{
	Name: "variable_sets",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Variable Sets", Category: "workspaces",
		Keywords:    "variable sets, varsets, shared variables, global variables",
		Description: "Variable sets of an organization with their scope and counts.",
		DocLLM: "One row per variable set in an organization (organization := argument). global = true means it " +
			"applies to every workspace. Read its variables with varset_variables(id).",
		DocMD: "## variable_sets(organization := ...)\n\nVariable sets: name, global flag, priority and counts of variables, workspaces and projects.",
		Examples: []vgi.CatalogExample{
			ex("Global variable sets", "SELECT name, var_count FROM terraform.main.variable_sets() WHERE global ORDER BY name"),
		},
	},
	Cols: []colDef{
		cID("id", "Variable set id (varset-...)."),
		cStr("name", "name", "Variable set name."),
		cStr("description", "description", "Description."),
		cBool("global", "global", "Whether it applies to every workspace of the organization."),
		cBool("priority", "priority", "Whether its values override workspace variables."),
		cInt("var_count", "var-count", "Number of variables in the set."),
		cInt("workspace_count", "workspace-count", "Number of workspaces it is attached to."),
		cInt("project_count", "project-count", "Number of projects it is attached to."),
		cTS("updated_at", "updated-at", "When the set last changed."),
		cParent("organization", "organization", "Organization name.", false),
	},
	Start: orgStart("varsets", nil),
}

type varsetArgs struct {
	VarsetID string `vgi:"name=varset_id,pos=0,doc=Variable set id (varset-...); list them with terraform.main.variable_sets()."`
}

var varsetVariablesDef = &hcpDef{
	Name: "varset_variables",
	Args: varsetArgs{},
	Docs: fnDocs{
		Title: "Variable Set Variables", Category: "workspaces",
		Keywords:    "variable set variables, varset vars, shared variables, sensitive",
		Description: "Variables of one variable set; sensitive values are always NULL.",
		DocLLM: "One row per variable in one variable set (varset_id positional, from variable_sets()). value is NULL " +
			"whenever sensitive is true.",
		DocMD: "## varset_variables(varset_id)\n\nVariables of a variable set: key, value (NULL when sensitive), category, HCL flag.",
		Examples: []vgi.CatalogExample{
			ex("Environment variables a variable set injects", "SELECT key, sensitive FROM terraform.main.varset_variables('varset-abc123') WHERE category = 'env' ORDER BY key"),
		},
	},
	Cols: []colDef{
		cID("id", "Variable id (var-...)."),
		cParent("varset_id", "varset_id", "Variable set id.", false),
		cStr("key", "key", "Variable name."),
		{column("value", tString, "Value; NULL when sensitive.", true), maskedValue},
		cBool("sensitive", "sensitive", "Whether the value is write-only (never returned)."),
		cStr("category", "category", "'terraform' or 'env'."),
		cBool("hcl", "hcl", "Whether the value is parsed as HCL."),
		cStr("description", "description", "Variable description."),
		cTS("created_at", "created-at", "When the variable was created."),
	},
	Start: func(_ context.Context, _ *client, p *vgi.ProcessParams, st *ScanState) error {
		var a varsetArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		id := strings.TrimSpace(a.VarsetID)
		if id == "" {
			return errEmptyArg("varset_id")
		}
		single(st, "/varsets/"+url.PathEscape(id)+"/relationships/vars", url.Values{}, map[string]string{"varset_id": id})
		return nil
	},
}

// ---- teams ------------------------------------------------------------------

var teamsDef = &hcpDef{
	Name: "teams",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Teams", Category: "organization",
		Keywords:    "teams, users, organization access, permissions, rbac",
		Description: "Teams of an organization with member counts and organization-level access flags.",
		DocLLM: "One row per team (organization := argument). organization_access is a JSON object of boolean " +
			"organization permissions (manage-workspaces, manage-policies, manage-providers, ...); use " +
			"organization_access->>'manage-workspaces' to test one. Workspace-level grants are in team_access(workspace_id).",
		DocMD: "## teams(organization := ...)\n\nTeams: name, visibility, user count, SSO team id and the organization access flags (JSON).",
		Examples: []vgi.CatalogExample{
			ex("Teams that can manage workspaces", "SELECT name, users_count FROM terraform.main.teams() WHERE organization_access->>'manage-workspaces' = 'true' ORDER BY name"),
		},
	},
	Cols: []colDef{
		cID("id", "Team id (team-...)."),
		cStr("name", "name", "Team name."),
		cStr("visibility", "visibility", "'secret' or 'organization'."),
		cInt("users_count", "users-count", "Number of members."),
		cStr("sso_team_id", "sso-team-id", "Linked SSO team id."),
		cJSON("organization_access", "organization-access", "Organization-level permissions as a JSON object of booleans."),
		cParent("organization", "organization", "Organization name.", false),
	},
	Start: orgStart("teams", nil),
}

var teamAccessDef = &hcpDef{
	Name: "team_access",
	Args: workspaceIDArgs{},
	Docs: fnDocs{
		Title: "Team Workspace Access", Category: "organization",
		Keywords:    "team access, workspace permissions, team-workspaces, rbac, admin",
		Description: "Which teams can access a workspace, and with what permissions.",
		DocLLM: "One row per team granted access to one workspace (workspace_id positional). access is read, plan, " +
			"write, admin or custom; the remaining columns detail custom permissions. Join team_id to teams().id for the team name.",
		DocMD: "## team_access(workspace_id)\n\nTeam grants on a workspace: access level and the run, variable and state permissions.",
		Examples: []vgi.CatalogExample{
			ex("Teams with admin access to a workspace", "SELECT a.team_id, t.name FROM terraform.main.team_access('ws-abc123') a JOIN terraform.main.teams() t ON t.id = a.team_id WHERE a.access = 'admin'"),
		},
	},
	Cols: []colDef{
		cID("id", "Team access id (tws-...)."),
		cRel("team_id", "team", "Team id."),
		cParent("workspace_id", "workspace_id", "Workspace id.", false),
		cStr("access", "access", "read, plan, write, admin or custom."),
		cStr("runs", "runs", "Run permission: read, plan or apply."),
		cStr("variables", "variables", "Variable permission: none, read or write."),
		cStr("state_versions", "state-versions", "State permission: none, read-outputs, read or write."),
		cStr("sentinel_mocks", "sentinel-mocks", "Sentinel mock permission: none or read."),
		cBool("workspace_locking", "workspace-locking", "Whether the team can lock the workspace."),
		cBool("run_tasks", "run-tasks", "Whether the team can manage run tasks."),
	},
	Start: workspaceStart(func(id string) (string, url.Values) {
		return "/team-workspaces", url.Values{"filter[workspace][id]": {id}}
	}),
}

// ---- agents -----------------------------------------------------------------

var agentPoolsDef = &hcpDef{
	Name: "agent_pools",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Agent Pools", Category: "organization",
		Keywords:    "agent pools, self-hosted agents, tfc-agent, private runners",
		Description: "Self-hosted agent pools of an organization with their agent counts.",
		DocLLM:      "One row per agent pool (organization := argument). List the agents of one pool with agents(agent_pool_id).",
		DocMD:       "## agent_pools(organization := ...)\n\nAgent pools: name, agent count and whether the pool is organization-scoped.",
		Examples: []vgi.CatalogExample{
			ex("Agent pools by size", "SELECT name, agent_count FROM terraform.main.agent_pools() ORDER BY agent_count DESC"),
		},
	},
	Cols: []colDef{
		cID("id", "Agent pool id (apool-...)."),
		cStr("name", "name", "Pool name."),
		cBool("organization_scoped", "organization-scoped", "Whether every workspace may use the pool."),
		cInt("agent_count", "agent-count", "Number of agents in the pool."),
		cTS("created_at", "created-at", "When the pool was created."),
		cParent("organization", "organization", "Organization name.", false),
	},
	Start: orgStart("agent-pools", nil),
}

type agentPoolArgs struct {
	AgentPoolID string `vgi:"name=agent_pool_id,pos=0,doc=Agent pool id (apool-...); list them with terraform.main.agent_pools()."`
}

var agentsDef = &hcpDef{
	Name: "agents",
	Args: agentPoolArgs{},
	Docs: fnDocs{
		Title: "Agents", Category: "organization",
		Keywords:    "agents, tfc-agent, agent status, last ping, self-hosted runners",
		Description: "Agents registered in one agent pool, with status and last ping time.",
		DocLLM: "One row per agent of one pool (agent_pool_id positional). status is idle, busy, unknown, exited or " +
			"errored; last_ping_at shows stale agents.",
		DocMD: "## agents(agent_pool_id)\n\nAgents of a pool: name, status, IP address and last ping.",
		Examples: []vgi.CatalogExample{
			ex("Agents that are not idle or busy", "SELECT name, status, last_ping_at FROM terraform.main.agents('apool-abc123') WHERE status NOT IN ('idle', 'busy') ORDER BY name"),
		},
	},
	Cols: []colDef{
		cID("id", "Agent id (agent-...)."),
		cParent("agent_pool_id", "agent_pool_id", "Agent pool id.", false),
		cStr("name", "name", "Agent name."),
		cStr("status", "status", "idle, busy, unknown, exited or errored."),
		cStr("ip_address", "ip-address", "IP address the agent connects from."),
		cTS("last_ping_at", "last-ping-at", "When the agent last checked in."),
	},
	Start: func(_ context.Context, _ *client, p *vgi.ProcessParams, st *ScanState) error {
		var a agentPoolArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		id := strings.TrimSpace(a.AgentPoolID)
		if id == "" {
			return errEmptyArg("agent_pool_id")
		}
		single(st, "/agent-pools/"+url.PathEscape(id)+"/agents", url.Values{}, map[string]string{"agent_pool_id": id})
		return nil
	},
}

// ---- private registry -------------------------------------------------------

var registryModulesDef = &hcpDef{
	Name: "registry_modules",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Private Registry Modules", Category: "registry",
		Keywords:    "private registry, modules, terraform modules, module versions",
		Description: "Modules published in an organization's private registry, with their versions.",
		DocLLM: "One row per module in the private registry (organization := argument). versions lists every " +
			"published version; registry_name is 'private' or 'public' (a curated public module).",
		DocMD: "## registry_modules(organization := ...)\n\nPrivate registry modules: name, namespace, provider, status and versions.",
		Examples: []vgi.CatalogExample{
			ex("Modules with their number of versions", "SELECT namespace, name, provider, len(versions) AS version_count FROM terraform.main.registry_modules() ORDER BY version_count DESC"),
		},
	},
	Cols: []colDef{
		cID("id", "Module id (mod-...)."),
		cStr("name", "name", "Module name."),
		cStr("namespace", "namespace", "Namespace (the organization for private modules)."),
		cStr("provider", "provider", "Provider the module targets, e.g. aws."),
		cStr("registry_name", "registry-name", "'private' or 'public'."),
		cStr("status", "status", "Publishing status, e.g. setup_complete."),
		{column("versions", tStrList, "Published versions.", true), func(r *resource, _ *rowCtx) any {
			items, ok := r.attr("version-statuses").([]any)
			if !ok {
				return nil
			}
			out := []any{}
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					if v, ok := m["version"].(string); ok {
						out = append(out, v)
					}
				}
			}
			return out
		}},
		cTS("created_at", "created-at", "When the module was published."),
		cTS("updated_at", "updated-at", "When the module last changed."),
	},
	Start: orgStart("registry-modules", nil),
}

var registryProvidersDef = &hcpDef{
	Name: "registry_providers",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Private Registry Providers", Category: "registry",
		Keywords:    "private registry, providers, terraform providers",
		Description: "Providers published in an organization's private registry.",
		DocLLM:      "One row per provider in the private registry (organization := argument).",
		DocMD:       "## registry_providers(organization := ...)\n\nPrivate registry providers: name, namespace and registry name.",
		Examples: []vgi.CatalogExample{
			ex("Private providers", "SELECT namespace, name FROM terraform.main.registry_providers() WHERE registry_name = 'private' ORDER BY name"),
		},
	},
	Cols: []colDef{
		cID("id", "Provider id (prov-...)."),
		cStr("name", "name", "Provider name."),
		cStr("namespace", "namespace", "Namespace."),
		cStr("registry_name", "registry-name", "'private' or 'public'."),
		cTS("created_at", "created-at", "When the provider was published."),
		cTS("updated_at", "updated-at", "When the provider last changed."),
	},
	Start: orgStart("registry-providers", nil),
}

// ---- notification configurations -------------------------------------------

// destinationHost keeps only scheme://host of a webhook URL: Slack and Teams
// webhook URLs carry their secret in the path.
func destinationHost(r *resource, _ *rowCtx) any {
	s, ok := r.attr("url").(string)
	if !ok || s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil
	}
	return u.Host
}

var notificationsDef = &hcpDef{
	Name: "notification_configurations",
	Args: workspaceIDArgs{},
	Docs: fnDocs{
		Title: "Notification Configurations", Category: "workspaces",
		Keywords:    "notifications, webhooks, slack, email, microsoft teams, alerts",
		Description: "Notification destinations of a workspace and the run events that trigger them; no secrets.",
		DocLLM: "One row per notification configuration of one workspace (workspace_id positional). triggers lists " +
			"events such as run:errored or assessment:drifted. Only the destination host is returned, never the full " +
			"webhook URL or token, because Slack/Teams webhook URLs embed their secret.",
		DocMD: "## notification_configurations(workspace_id)\n\nNotification destinations: type, enabled, triggers and " +
			"the destination host (webhook URLs and tokens are never returned).",
		Examples: []vgi.CatalogExample{
			ex("Enabled destinations notified on drift", "SELECT name, destination_type FROM terraform.main.notification_configurations('ws-abc123') WHERE enabled AND list_contains(triggers, 'assessment:drifted')"),
		},
	},
	Cols: []colDef{
		cID("id", "Notification configuration id (nc-...)."),
		cParent("workspace_id", "workspace_id", "Workspace id.", false),
		cStr("name", "name", "Name."),
		cStr("destination_type", "destination-type", "generic, email, slack or microsoft-teams."),
		cBool("enabled", "enabled", "Whether notifications are sent."),
		cList("triggers", "triggers", "Events that notify, e.g. run:errored, assessment:drifted."),
		{column("destination_host", tString, "Host of the webhook URL only (the path can hold a secret); NULL for email.", true), destinationHost},
		cTS("created_at", "created-at", "When it was created."),
		cTS("updated_at", "updated-at", "When it last changed."),
	},
	Start: workspaceStart(func(id string) (string, url.Values) {
		return "/workspaces/" + url.PathEscape(id) + "/notification-configurations", url.Values{}
	}),
}

// ---- assessments (drift detection) ------------------------------------------

var assessmentsDef = &hcpDef{
	Name: "assessments",
	Args: orgArgs{},
	Docs: fnDocs{
		Title: "Health Assessments", Category: "workspaces",
		Keywords:    "drift detection, health assessments, drifted, continuous validation, checks",
		Description: "Latest health assessment (drift detection) result of every workspace that has assessments enabled or a result.",
		DocLLM: "One row per workspace of an organization (organization := argument) that has health assessments " +
			"enabled or a past result. drifted = true means real infrastructure no longer matches state; succeeded = " +
			"false means the assessment itself failed (error_message). It reads the workspace list and then each " +
			"workspace's current assessment result (one request per workspace with a result).",
		DocMD: "## assessments(organization := ...)\n\nThe current drift-detection result per workspace: drifted, succeeded, error message and time.",
		Examples: []vgi.CatalogExample{
			ex("Drifted workspaces", "SELECT workspace_name, created_at FROM terraform.main.assessments() WHERE drifted ORDER BY workspace_name"),
		},
	},
	Cols: []colDef{
		cParent("workspace_id", "workspace_id", "Workspace id.", false),
		cParent("workspace_name", "workspace_name", "Workspace name.", true),
		{column("assessments_enabled", tBool, "Whether health assessments are enabled on the workspace.", true),
			func(_ *resource, x *rowCtx) any { return x.step.Ctx["assessments_enabled"] == "true" }},
		{column("assessment_result_id", tString, "Id of the current assessment result (asmtres-...); NULL before the first assessment.", true),
			func(r *resource, _ *rowCtx) any {
				if r == nil {
					return nil
				}
				return r.ID
			}},
		cBool("drifted", "drifted", "Whether resources drifted from state."),
		cBool("succeeded", "succeeded", "Whether the assessment ran successfully."),
		cStr("error_message", "error-msg", "Why the assessment failed."),
		cTS("created_at", "created-at", "When the assessment ran."),
	},
	Start: orgStart("workspaces", nil),
}

func init() { assessmentsDef.Handle = assessmentsHandle }

// assessmentsHandle lists workspaces, then reads each current assessment result.
func assessmentsHandle(x *rowCtx, res []resource, _ *ScanState) ([][]any, []Step, bool) {
	var rows [][]any
	var more []Step
	if x.step.Kind == "result" {
		for i := range res {
			rows = append(rows, assessmentsDef.row(&res[i], x))
		}
		return rows, nil, false
	}
	for i := range res {
		w := &res[i]
		name, _ := w.str("name").(string)
		enabled, _ := w.boolean("assessments-enabled").(bool)
		ctx := map[string]string{"workspace_id": w.ID, "workspace_name": name}
		if enabled {
			ctx["assessments_enabled"] = "true"
		}
		if rid := w.relID("current-assessment-result"); rid != "" {
			more = append(more, Step{Path: "/assessment-results/" + url.PathEscape(rid), Kind: "result", Ctx: ctx})
		} else if enabled {
			rows = append(rows, assessmentsDef.row(nil, &rowCtx{c: x.c, doc: x.doc, step: &Step{Ctx: ctx}}))
		}
	}
	return rows, more, false
}
