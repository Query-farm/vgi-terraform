// Run tables: runs, plans, applies, cost estimates, policy checks and
// policy evaluations.
//
// runs / plans / applies / cost_estimates share one scan: the workspace or
// organization run listing with include=plan,apply,cost_estimate,workspace,
// so plans and applies cost no extra requests (the run listing endpoints are
// limited to 30 requests per minute). Runs come newest first, so a lower
// bound on created_at stops the listing early.

package terraform

import (
	"context"
	"net/url"
	"strings"

	"github.com/Query-farm/vgi-go/vgi"
)

type runsArgs struct {
	WorkspaceID  *string `vgi:"name=workspace_id,doc=List one workspace's runs (ws-...); default every run of the organization."`
	Organization *string `vgi:"name=organization,doc=Organization name; default the ORGANIZATION of the terraform secret. Ignored when workspace_id is set."`
}

const runIncludes = "plan,apply,cost_estimate,workspace"

// runsStart queues the run listing, translating pushed filters on the named
// columns into filter[status] / filter[source] / filter[workspace_names] and
// a created-at lower bound.
func runsStart(statusCol, sourceCol, workspaceCol, createdCol string, mapStatus func([]string) ([]string, bool)) func(context.Context, *client, *vgi.ProcessParams, *ScanState) error {
	return func(_ context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error {
		var a runsArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		pf := p.CurrentPushdownFilters
		q := url.Values{"include": {runIncludes}}
		if statusCol != "" {
			if v, ok := eqStrings(pf, statusCol); ok {
				if mapStatus != nil {
					v, ok = mapStatus(v)
				}
				if ok {
					q.Set("filter[status]", strings.Join(v, ","))
				}
			}
		}
		if sourceCol != "" {
			if v, ok := eqStrings(pf, sourceCol); ok {
				q.Set("filter[source]", strings.Join(v, ","))
			}
		}
		if createdCol != "" {
			setMin(st, pf, createdCol)
		}
		if a.WorkspaceID != nil && strings.TrimSpace(*a.WorkspaceID) != "" {
			id := strings.TrimSpace(*a.WorkspaceID)
			single(st, "/workspaces/"+url.PathEscape(id)+"/runs", q, map[string]string{"workspace_id": id})
			return nil
		}
		org, err := c.cfg.org(a.Organization)
		if err != nil {
			return err
		}
		if workspaceCol != "" {
			if v, ok := eqStrings(pf, workspaceCol); ok {
				q.Set("filter[workspace_names]", strings.Join(v, ","))
			}
		}
		single(st, "/organizations/"+url.PathEscape(org)+"/runs", q, map[string]string{"organization": org})
		return nil
	}
}

// runsHandle builds one row per run with rowFn and stops at the created-at bound.
func runsHandle(rowFn func(run *resource, x *rowCtx) []any) func(*rowCtx, []resource, *ScanState) ([][]any, []Step, bool) {
	return func(x *rowCtx, res []resource, st *ScanState) ([][]any, []Step, bool) {
		var rows [][]any
		stop := false
		for i := range res {
			if pastMin(st, res[i].ts("created-at")) {
				stop = true
			}
			if r := rowFn(&res[i], x); r != nil {
				rows = append(rows, r)
			}
		}
		return rows, nil, stop
	}
}

// runWorkspace is the run's included workspace.
func runWorkspace(run *resource, x *rowCtx) *resource {
	return x.doc.included("workspaces", run.relID("workspace"))
}

// runURL links a run in the UI.
func runURL(run *resource, x *rowCtx) any {
	ws := runWorkspace(run, x)
	name, _ := ws.str("name").(string)
	org := ws.relID("organization")
	if org == "" {
		org = x.step.Ctx["organization"]
	}
	base, ok := x.c.cfg.workspaceURL(org, name).(string)
	if !ok {
		return nil
	}
	return base + "/runs/" + url.PathEscape(run.ID)
}

func runWorkspaceName(run *resource, x *rowCtx) any { return runWorkspace(run, x).str("name") }

var runCols = []colDef{
	cID("id", "Run id (run-...)."),
	cRel("workspace_id", "workspace", "Workspace id (ws-...)."),
	{column("workspace_name", tString, "Workspace name.", true), runWorkspaceName},
	cStr("status", "status", "Run status: pending, planning, planned, cost_estimated, policy_checked, applying, applied, planned_and_finished, errored, canceled, discarded, ..."),
	cStr("source", "source", "What created the run: tfe-ui, tfe-api or tfe-configuration-version (VCS push / CLI upload)."),
	cStr("trigger_reason", "trigger-reason", "Why the run started, e.g. manual."),
	cStr("message", "message", "Run message."),
	cBool("is_destroy", "is-destroy", "Whether the run destroys all resources."),
	cBool("plan_only", "plan-only", "Whether the run is a speculative plan."),
	cBool("refresh_only", "refresh-only", "Whether the run only refreshes state."),
	cBool("has_changes", "has-changes", "Whether the plan has changes."),
	cBool("auto_apply", "auto-apply", "Whether the run applies automatically."),
	cTS("created_at", "created-at", "When the run was created (queued)."),
	cJSON("status_timestamps", "status-timestamps", "When the run entered each status, e.g. {\"planning-at\": ..., \"applied-at\": ...}."),
	cRel("plan_id", "plan", "Plan id (plan-...)."),
	cRel("apply_id", "apply", "Apply id (apply-...)."),
	cRel("cost_estimate_id", "cost-estimate", "Cost estimate id (ce-...)."),
	{column("resource_additions", tInt64, "Resources the plan adds.", true),
		func(r *resource, x *rowCtx) any {
			return x.doc.included("plans", r.relID("plan")).i64("resource-additions")
		}},
	{column("resource_changes", tInt64, "Resources the plan changes in place.", true),
		func(r *resource, x *rowCtx) any {
			return x.doc.included("plans", r.relID("plan")).i64("resource-changes")
		}},
	{column("resource_destructions", tInt64, "Resources the plan destroys.", true),
		func(r *resource, x *rowCtx) any {
			return x.doc.included("plans", r.relID("plan")).i64("resource-destructions")
		}},
	{column("resource_imports", tInt64, "Resources the plan imports.", true),
		func(r *resource, x *rowCtx) any {
			return x.doc.included("plans", r.relID("plan")).i64("resource-imports")
		}},
	{column("url", tString, "Link to the run in the HCP Terraform / TFE UI.", true), runURL},
}

var runsDef = &hcpDef{
	Name:   "runs",
	Args:   runsArgs{},
	Filter: true,
	Cols:   runCols,
	Docs: fnDocs{
		Title: "Runs", Category: "runs",
		Keywords:    "runs, terraform runs, plan, apply, deployments, errored runs, run status, run history",
		Description: "Runs of a workspace or a whole organization, newest first, with plan add/change/destroy counts.",
		DocLLM: "One row per run, newest first: of one workspace (workspace_id := 'ws-...') or of the whole " +
			"organization (organization :=, default the secret's ORGANIZATION). Pushdown: status = / IN (...) becomes " +
			"filter[status], source = becomes filter[source], workspace_name = / IN becomes filter[workspace_names] " +
			"(organization listing), and created_at >= / > a timestamp stops paging once older runs are reached, so " +
			"'runs in the last 7 days' reads only those pages. The plan's resource_additions / resource_changes / " +
			"resource_destructions / resource_imports come with the run. The API omits speculative plan-only runs from " +
			"workspace listings. The run listing is rate-limited by HCP Terraform to 30 requests per minute; the worker " +
			"retries 429 responses.",
		DocMD: "## runs(workspace_id := ..., organization := ...)\n\nRuns, newest first, with status, source, trigger, " +
			"destroy/plan-only flags, status timestamps and the plan's resource counts.\n\n**Pushdown:** `status`, " +
			"`source`, `workspace_name` (= / IN) and a `created_at` lower bound.",
		Examples: []vgi.CatalogExample{
			ex("Errored runs per workspace", "SELECT workspace_name, count(*) AS errored_runs, max(created_at) AS latest FROM terraform.main.runs() WHERE status = 'errored' GROUP BY workspace_name ORDER BY errored_runs DESC"),
			ex("Resources destroyed per workspace by applied runs", "SELECT workspace_name, sum(resource_destructions) AS destroyed FROM terraform.main.runs() WHERE status = 'applied' GROUP BY workspace_name ORDER BY destroyed DESC"),
		},
	},
	Start: runsStart("status", "source", "workspace_name", "created_at", nil),
}

func init() {
	runsDef.Handle = runsHandle(runsDef.row)
	plansDef.Handle = includedHandle(plansDef, "plans", "plan")
	appliesDef.Handle = includedHandle(appliesDef, "applies", "apply")
	costEstimatesDef.Handle = includedHandle(costEstimatesDef, "cost-estimates", "cost-estimate")
}

// includedHandle emits one row per run's included plan / apply / cost estimate.
func includedHandle(def *hcpDef, typ, rel string) func(*rowCtx, []resource, *ScanState) ([][]any, []Step, bool) {
	return runsHandle(func(run *resource, x *rowCtx) []any {
		inc := x.doc.included(typ, run.relID(rel))
		if inc == nil {
			return nil
		}
		row := def.row(inc, x)
		// The run columns read the run, not the included object.
		for i, c := range def.Cols {
			switch c.F.Name {
			case "run_id":
				row[i] = run.ID
			case "workspace_id":
				row[i] = nilIfEmpty(run.relID("workspace"))
			case "workspace_name":
				row[i] = runWorkspaceName(run, x)
			case "run_created_at":
				row[i] = run.ts("created-at")
			}
		}
		return row
	})
}

var runRefCols = []colDef{
	{column("run_id", tString, "Run id (run-...).", false), nil},
	{column("workspace_id", tString, "Workspace id.", true), nil},
	{column("workspace_name", tString, "Workspace name.", true), nil},
	{column("run_created_at", tTS, "When the run was created.", true), nil},
}

func withRunRef(cols ...colDef) []colDef {
	out := []colDef{cols[0]}
	out = append(out, runRefCols...)
	return append(out, cols[1:]...)
}

var plansDef = &hcpDef{
	Name: "plans",
	Args: runsArgs{},
	Docs: fnDocs{
		Title: "Plans", Category: "runs",
		Keywords:    "plans, terraform plan, resource additions, changes, destructions, imports",
		Description: "The plan of every run (newest run first): status, timings and resource add/change/destroy/import counts.",
		DocLLM: "One row per run's plan, same arguments as runs (workspace_id := or organization :=). Read from the " +
			"run listing, so it costs no extra requests. has_changes and the resource counts say what the plan " +
			"would do; status is pending, queued, running, finished, errored, canceled or unreachable.",
		DocMD: "## plans(workspace_id := ..., organization := ...)\n\nPlans with status, queued/started/finished times and resource counts.",
		Examples: []vgi.CatalogExample{
			ex("Plans that would destroy resources", "SELECT workspace_name, run_id, resource_destructions FROM terraform.main.plans() WHERE resource_destructions > 0 ORDER BY run_created_at DESC"),
		},
	},
	Cols: withRunRef(
		cID("id", "Plan id (plan-...)."),
		cStr("status", "status", "pending, managed_queued, queued, running, errored, canceled, finished or unreachable."),
		cBool("has_changes", "has-changes", "Whether the plan has changes."),
		cInt("resource_additions", "resource-additions", "Resources to add."),
		cInt("resource_changes", "resource-changes", "Resources to change in place."),
		cInt("resource_destructions", "resource-destructions", "Resources to destroy."),
		cInt("resource_imports", "resource-imports", "Resources to import."),
		cTS("queued_at", "status-timestamps.queued-at", "When the plan was queued."),
		cTS("started_at", "status-timestamps.started-at", "When the plan started."),
		cTS("finished_at", "status-timestamps.finished-at", "When the plan finished."),
	),
	Start: runsStart("", "", "", "run_created_at", nil),
}

var appliesDef = &hcpDef{
	Name: "applies",
	Args: runsArgs{},
	Docs: fnDocs{
		Title: "Applies", Category: "runs",
		Keywords:    "applies, terraform apply, applied resources",
		Description: "The apply of every run (newest run first): status, timings and the resources it added, changed and destroyed.",
		DocLLM: "One row per run's apply, same arguments as runs. status 'unreachable' means the apply never ran " +
			"(plan-only run, or the run stopped earlier).",
		DocMD: "## applies(workspace_id := ..., organization := ...)\n\nApplies with status, times and resource counts.",
		Examples: []vgi.CatalogExample{
			ex("Failed applies", "SELECT workspace_name, run_id, finished_at FROM terraform.main.applies() WHERE status = 'errored' ORDER BY run_created_at DESC"),
		},
	},
	Cols: withRunRef(
		cID("id", "Apply id (apply-...)."),
		cStr("status", "status", "pending, managed_queued, queued, running, errored, canceled, finished or unreachable."),
		cInt("resource_additions", "resource-additions", "Resources added."),
		cInt("resource_changes", "resource-changes", "Resources changed."),
		cInt("resource_destructions", "resource-destructions", "Resources destroyed."),
		cInt("resource_imports", "resource-imports", "Resources imported."),
		cTS("queued_at", "status-timestamps.queued-at", "When the apply was queued."),
		cTS("started_at", "status-timestamps.started-at", "When the apply started."),
		cTS("finished_at", "status-timestamps.finished-at", "When the apply finished."),
	),
	Start: runsStart("", "", "", "run_created_at", nil),
}

var costEstimatesDef = &hcpDef{
	Name: "cost_estimates",
	Args: runsArgs{},
	Docs: fnDocs{
		Title: "Cost Estimates", Category: "runs",
		Keywords:    "cost estimation, monthly cost, finops, cost delta",
		Description: "The cost estimate of every run (newest run first): prior, proposed and delta monthly cost.",
		DocLLM: "One row per run that has a cost estimate, same arguments as runs. Costs are monthly USD as `DOUBLE`; " +
			"delta_monthly_cost = proposed - prior. Only organizations with cost estimation enabled produce rows.",
		DocMD: "## cost_estimates(workspace_id := ..., organization := ...)\n\nCost estimates: prior, proposed and delta monthly cost and matched resource counts.",
		Examples: []vgi.CatalogExample{
			ex("Runs that raise monthly cost by more than $100", "SELECT workspace_name, run_id, delta_monthly_cost FROM terraform.main.cost_estimates() WHERE delta_monthly_cost > 100 ORDER BY delta_monthly_cost DESC"),
		},
	},
	Cols: withRunRef(
		cID("id", "Cost estimate id (ce-...)."),
		cStr("status", "status", "pending, queued, finished, errored, canceled or skipped_due_to_targeting."),
		cFloat("prior_monthly_cost", "prior-monthly-cost", "Monthly cost before the run (USD)."),
		cFloat("proposed_monthly_cost", "proposed-monthly-cost", "Monthly cost after the run (USD)."),
		cFloat("delta_monthly_cost", "delta-monthly-cost", "proposed - prior (USD)."),
		cInt("resources_count", "resources-count", "Resources considered."),
		cInt("matched_resources_count", "matched-resources-count", "Resources with a known price."),
		cInt("unmatched_resources_count", "unmatched-resources-count", "Resources without a price."),
		cStr("error_message", "error-message", "Why the estimate failed."),
	),
	Start: runsStart("", "", "", "run_created_at", nil),
}

// ---- policy checks / evaluations -------------------------------------------

type runIDArgs struct {
	RunID string `vgi:"name=run_id,pos=0,doc=Run id (run-...); list them with terraform.main.runs()."`
}

func runStart(mk func(id string) string) func(context.Context, *client, *vgi.ProcessParams, *ScanState) error {
	return func(_ context.Context, _ *client, p *vgi.ProcessParams, st *ScanState) error {
		var a runIDArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		id := strings.TrimSpace(a.RunID)
		if id == "" {
			return errEmptyArg("run_id")
		}
		st.Steps = append(st.Steps, Step{Path: mk(id), Query: "", Paged: true, Kind: "first", Ctx: map[string]string{"run_id": id}})
		return nil
	}
}

var policyChecksDef = &hcpDef{
	Name: "policy_checks",
	Args: runIDArgs{},
	Docs: fnDocs{
		Title: "Sentinel Policy Checks", Category: "runs",
		Keywords:    "sentinel, policy checks, policy as code, hard failed, soft failed, advisory",
		Description: "Sentinel policy checks of one run with pass/fail counts by enforcement level.",
		DocLLM: "One row per (legacy) Sentinel policy check of one run (run_id positional). status is passed, " +
			"soft_failed, hard_failed, overridden, errored, ...; result is true when every policy passed. For OPA and " +
			"the newer Sentinel policy evaluations use policy_evaluations(run_id).",
		DocMD: "## policy_checks(run_id)\n\nSentinel policy checks: status, scope, result and passed / hard / soft / advisory failure counts.",
		Examples: []vgi.CatalogExample{
			ex("Hard-failed policies of a run", "SELECT id, hard_failed, soft_failed FROM terraform.main.policy_checks('run-abc123') WHERE hard_failed > 0"),
		},
	},
	Cols: []colDef{
		cID("id", "Policy check id (polchk-...)."),
		cParent("run_id", "run_id", "Run id.", false),
		cStr("status", "status", "queued, passed, soft_failed, hard_failed, overridden, errored, canceled, unreachable."),
		cStr("scope", "scope", "organization or workspace."),
		cBool("result", "result.result", "Whether every policy passed."),
		cInt("passed", "result.passed", "Policies that passed."),
		cInt("total_failed", "result.total-failed", "Policies that failed."),
		cInt("hard_failed", "result.hard-failed", "Hard-mandatory policies that failed."),
		cInt("soft_failed", "result.soft-failed", "Soft-mandatory policies that failed."),
		cInt("advisory_failed", "result.advisory-failed", "Advisory policies that failed."),
		cInt("duration_ms", "result.duration-ms", "Evaluation time in milliseconds."),
		cJSON("status_timestamps", "status-timestamps", "When the check entered each status."),
	},
	Start: runStart(func(id string) string { return "/runs/" + url.PathEscape(id) + "/policy-checks" }),
}

var policyEvaluationsDef = &hcpDef{
	Name: "policy_evaluations",
	Args: runIDArgs{},
	Docs: fnDocs{
		Title: "Policy Evaluations", Category: "runs",
		Keywords:    "opa, sentinel, policy evaluations, policy sets, mandatory failed, task stages",
		Description: "OPA and Sentinel policy evaluations of one run (from its task stages), with result counts.",
		DocLLM: "One row per policy evaluation of one run (run_id positional): the run's task stages (pre_plan, " +
			"post_plan, pre_apply, ...) are read, then each stage's policy evaluations. policy_kind is opa or " +
			"sentinel; mandatory_failed > 0 blocks the run unless overridden.",
		DocMD: "## policy_evaluations(run_id)\n\nPolicy evaluations per task stage: kind, status and passed / advisory / mandatory / errored counts.",
		Examples: []vgi.CatalogExample{
			ex("Blocking OPA evaluations of a run", "SELECT stage, status, mandatory_failed FROM terraform.main.policy_evaluations('run-abc123') WHERE policy_kind = 'opa' AND mandatory_failed > 0"),
		},
	},
	Cols: []colDef{
		cID("id", "Policy evaluation id (poleval-...)."),
		cParent("run_id", "run_id", "Run id.", false),
		cParent("task_stage_id", "task_stage_id", "Task stage id (ts-...).", false),
		cParent("stage", "stage", "Task stage: pre_plan, post_plan, pre_apply or post_apply.", true),
		cStr("status", "status", "pending, queued, running, passed, failed, errored, canceled, overridden, unreachable."),
		cStr("policy_kind", "policy-kind", "opa or sentinel."),
		cStr("policy_tool_version", "policy-tool-version", "OPA / Sentinel version."),
		cInt("passed", "result-count.passed", "Policies that passed."),
		cInt("advisory_failed", "result-count.advisory-failed", "Advisory policies that failed."),
		cInt("mandatory_failed", "result-count.mandatory-failed", "Mandatory policies that failed."),
		cInt("errored", "result-count.errored", "Policies that errored."),
		cTS("created_at", "created-at", "When the evaluation was created."),
		cTS("updated_at", "updated-at", "When it last changed."),
	},
	Start: runStart(func(id string) string { return "/runs/" + url.PathEscape(id) + "/task-stages" }),
}

func init() { policyEvaluationsDef.Handle = policyEvaluationsHandle }

// policyEvaluationsHandle turns the task-stage listing into per-stage requests.
func policyEvaluationsHandle(x *rowCtx, res []resource, _ *ScanState) ([][]any, []Step, bool) {
	if x.step.Kind != "first" {
		var rows [][]any
		for i := range res {
			rows = append(rows, policyEvaluationsDef.row(&res[i], x))
		}
		return rows, nil, false
	}
	var more []Step
	for i := range res {
		stage, _ := res[i].str("stage").(string)
		more = append(more, Step{
			Path: "/task-stages/" + url.PathEscape(res[i].ID) + "/policy-evaluations", Paged: true, Kind: "evals",
			Ctx: map[string]string{"run_id": x.step.Ctx["run_id"], "task_stage_id": res[i].ID, "stage": stage},
		})
	}
	return nil, more, false
}
