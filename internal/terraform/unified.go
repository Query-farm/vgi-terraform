// unified.runs / unified.task_runs: the cross-orchestrator shape shared by
// the airflow, dagster, prefect, temporal, databricks, dbt and github
// workers, so runs can be UNION ALLed across tools. For Terraform a run is a
// deployment of infrastructure; its plan and apply are its task runs.
//
//   - runs: one row per run of the secret's ORGANIZATION. status / raw_status
//     (= / IN) and pipeline (workspace name) push down into filter[status] /
//     filter[workspace_names]; a queued_at lower bound stops paging early.
//   - task_runs: two rows per run (plan, apply) from the same listing; a
//     run_id = / IN filter reads just those runs.

package terraform

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Query-farm/vgi-go/vgi"
)

// rawStatusClass groups HCP Terraform run statuses into the shared value set.
var rawStatusClass = map[string]string{
	"applied": "success", "planned_and_finished": "success", "planned_and_saved": "success",
	"errored":  "failed",
	"canceled": "cancelled", "force_canceled": "cancelled", "discarded": "cancelled",
	"pending": "queued", "fetching": "queued", "fetching_completed": "queued", "queuing": "queued",
	"plan_queued": "queued", "apply_queued": "queued", "queuing_apply": "queued", "confirmed": "queued",
}

// unifiedRunStatus maps a run status; anything not final or queued is running.
func unifiedRunStatus(raw string) string {
	if c, ok := rawStatusClass[raw]; ok {
		return c
	}
	return "running"
}

// rawStatusesFor maps unified status literals to raw statuses for
// filter[status]; running is an open set, so it cannot push down.
func rawStatusesFor(vals []string) ([]string, bool) {
	var out []string
	for _, v := range vals {
		if v == "running" {
			return nil, false
		}
		n := len(out)
		for raw, c := range rawStatusClass {
			if c == v {
				out = append(out, raw)
			}
		}
		if len(out) == n {
			// An impossible value: keep it so the API returns nothing for it.
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out, true
}

// runTrigger classifies the run source.
func runTrigger(source string) any {
	switch source {
	case "tfe-ui":
		return "manual"
	case "tfe-api":
		return "api"
	case "tfe-configuration-version":
		return "event"
	case "":
		return nil
	}
	return "other"
}

var runEndKeys = []string{"applied-at", "planned-and-finished-at", "planned-and-saved-at", "errored-at",
	"canceled-at", "force-canceled-at", "discarded-at"}

func runStartedAt(r *resource) any {
	for _, k := range []string{"plan-queued-at", "planning-at", "applying-at"} {
		if t := r.ts("status-timestamps." + k); t != nil {
			return t
		}
	}
	return nil
}

func runEndedAt(r *resource) any {
	status, _ := r.str("status").(string)
	if c := unifiedRunStatus(status); c == "running" || c == "queued" {
		return nil
	}
	var latest any
	for _, k := range runEndKeys {
		if t, ok := r.ts("status-timestamps." + k).(time.Time); ok {
			if l, ok := latest.(time.Time); !ok || t.After(l) {
				latest = t
			}
		}
	}
	return latest
}

func constCol(name, value, doc string) colDef {
	return colDef{column(name, tString, doc, false), func(*resource, *rowCtx) any { return value }}
}

var unifiedRunsDef = &hcpDef{
	Name:   "runs",
	Filter: true,
	Docs: fnDocs{
		Title: "Unified Terraform Runs", Category: "unified",
		Keywords:    "unified, runs, deployments, orchestrator, union all, terraform runs",
		Description: "HCP Terraform runs (infrastructure deployments) of the secret's organization in the shared orchestrator run shape.",
		DocLLM: "One row per run of the terraform secret's ORGANIZATION, newest first, in the column layout shared by the " +
			"Query.Farm orchestrator workers (UNION ALL with airflow, dagster, prefect, temporal, ...). pipeline is the " +
			"workspace name; status is queued, running, success, failed or cancelled (applied / planned_and_finished / " +
			"planned_and_saved are success, errored is failed, canceled / force_canceled / discarded are cancelled); " +
			"raw_status keeps the HCP Terraform status. trigger is manual (UI), api, event (VCS push or CLI upload) or other. " +
			"Pushdown: status / raw_status / pipeline (= or IN) and a queued_at lower bound.",
		DocMD: "## unified.runs\n\nTerraform runs in the cross-orchestrator shape: a run is one deployment of a " +
			"workspace's infrastructure. `pipeline` is the workspace name.",
		Examples: []vgi.CatalogExample{
			ex("Failed deployments per workspace", "SELECT pipeline, count(*) AS failures FROM terraform.unified.runs WHERE status = 'failed' GROUP BY pipeline ORDER BY failures DESC"),
		},
	},
	Cols: []colDef{
		constCol("orchestrator", "terraform", "Always 'terraform'."),
		{column("run_id", tString, "Run id (run-...).", false), func(r *resource, _ *rowCtx) any { return r.ID }},
		{column("pipeline", tString, "Workspace name.", true), runWorkspaceName},
		{column("status", tString, "queued, running, success, failed or cancelled.", false), func(r *resource, _ *rowCtx) any {
			s, _ := r.str("status").(string)
			return unifiedRunStatus(s)
		}},
		cStr("raw_status", "status", "HCP Terraform run status, e.g. applied, errored, planned_and_finished."),
		{column("trigger", tString, "manual (UI), api, event (VCS push or CLI upload) or other.", true), func(r *resource, _ *rowCtx) any {
			s, _ := r.str("source").(string)
			return runTrigger(s)
		}},
		cTS("queued_at", "created-at", "When the run was created."),
		{column("started_at", tTS, "When the plan was queued or started.", true), func(r *resource, _ *rowCtx) any { return runStartedAt(r) }},
		{column("ended_at", tTS, "When the run reached a final status; NULL while active.", true), func(r *resource, _ *rowCtx) any { return runEndedAt(r) }},
		{column("duration_seconds", tFloat64, "Seconds from started_at (or queued_at) to ended_at.", true), func(r *resource, _ *rowCtx) any {
			start := runStartedAt(r)
			if start == nil {
				start = r.ts("created-at")
			}
			return durationBetween(start, runEndedAt(r))
		}},
		{column("url", tString, "Link to the run in the HCP Terraform / TFE UI.", true), runURL},
	},
	Start: runsStart("status", "", "pipeline", "queued_at", rawStatusesFor),
}

// unifiedRawStart adds raw_status pushdown on top of runsStart.
func unifiedRawStart(ctx context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error {
	if err := runsStart("status", "", "pipeline", "queued_at", rawStatusesFor)(ctx, c, p, st); err != nil {
		return err
	}
	if v, ok := eqStrings(p.CurrentPushdownFilters, "raw_status"); ok && len(st.Steps) == 1 {
		q, _ := url.ParseQuery(st.Steps[0].Query)
		if _, has := q["filter[status]"]; !has {
			sort.Strings(v)
			q.Set("filter[status]", strings.Join(v, ","))
			st.Steps[0].Query = q.Encode()
		}
	}
	return nil
}

// planApplyStatus maps a plan / apply status to the shared value set.
func planApplyStatus(raw string) string {
	switch raw {
	case "pending", "managed_queued", "queued":
		return "queued"
	case "running":
		return "running"
	case "finished":
		return "success"
	case "errored":
		return "failed"
	case "canceled":
		return "cancelled"
	case "unreachable":
		return "skipped"
	}
	return "running"
}

func phaseEnded(r *resource) any {
	for _, k := range []string{"finished-at", "errored-at", "canceled-at"} {
		if t := r.ts("status-timestamps." + k); t != nil {
			return t
		}
	}
	return nil
}

var unifiedTaskRunsDef = &hcpDef{
	Name:   "task_runs",
	Filter: true,
	Docs: fnDocs{
		Title: "Unified Terraform Task Runs", Category: "unified",
		Keywords:    "unified, task runs, plan, apply, orchestrator, union all",
		Description: "The plan and apply phases of every HCP Terraform run in the shared orchestrator task-run shape.",
		DocLLM: "Two rows per run of the secret's ORGANIZATION: task_name 'plan' and 'apply' (task_run_id is the plan / " +
			"apply id). status maps finished to success, errored to failed, canceled to cancelled and unreachable " +
			"(the phase never ran, e.g. plan-only runs) to skipped. Read from the same listing as unified.runs; a " +
			"run_id = / IN filter fetches just those runs.",
		DocMD: "## unified.task_runs\n\nPlan and apply phases of each run in the cross-orchestrator shape.",
		Examples: []vgi.CatalogExample{
			ex("Average plan and apply duration", "SELECT task_name, avg(duration_seconds) AS avg_seconds FROM terraform.unified.task_runs WHERE status = 'success' GROUP BY task_name ORDER BY task_name"),
		},
	},
	Cols: []colDef{
		constCol("orchestrator", "terraform", "Always 'terraform'."),
		{column("run_id", tString, "Run id (run-...).", false), nil},
		{column("task_run_id", tString, "Plan id (plan-...) or apply id (apply-...).", false), nil},
		{column("task_name", tString, "'plan' or 'apply'.", true), nil},
		{column("status", tString, "queued, running, success, failed, cancelled or skipped.", false), nil},
		{column("raw_status", tString, "Plan / apply status: pending, queued, running, finished, errored, canceled, unreachable.", true), nil},
		{column("attempt", tInt32, "Always 1: a Terraform run phase is not retried in place.", true), nil},
		{column("started_at", tTS, "When the phase started.", true), nil},
		{column("ended_at", tTS, "When the phase finished, errored or was canceled.", true), nil},
		{column("duration_seconds", tFloat64, "Seconds from started_at to ended_at.", true), nil},
		{column("url", tString, "Link to the run in the HCP Terraform / TFE UI.", true), nil},
	},
	Start: func(ctx context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error {
		if ids, ok := eqStrings(p.CurrentPushdownFilters, "run_id"); ok {
			for _, id := range ids {
				st.Steps = append(st.Steps, Step{Path: "/runs/" + url.PathEscape(id), Query: url.Values{"include": {runIncludes}}.Encode()})
			}
			return nil
		}
		return runsStart("", "", "", "", nil)(ctx, c, p, st)
	},
	Handle: func(x *rowCtx, res []resource, _ *ScanState) ([][]any, []Step, bool) {
		var rows [][]any
		for i := range res {
			run := &res[i]
			link := runURL(run, x)
			for _, phase := range []struct{ name, typ, rel string }{{"plan", "plans", "plan"}, {"apply", "applies", "apply"}} {
				inc := x.doc.included(phase.typ, run.relID(phase.rel))
				if inc == nil {
					continue
				}
				raw, _ := inc.str("status").(string)
				started, ended := inc.ts("status-timestamps.started-at"), phaseEnded(inc)
				rows = append(rows, []any{"terraform", run.ID, inc.ID, phase.name, planApplyStatus(raw), nilIfEmpty(raw),
					int64(1), started, ended, durationBetween(started, ended), link})
			}
		}
		return rows, nil, false
	},
}

func init() {
	unifiedRunsDef.Start = unifiedRawStart
	unifiedRunsDef.Handle = runsHandle(unifiedRunsDef.row)
}

// catalogTable wraps a definition as a catalog table with a primary key and
// optional foreign keys (column -> table).
func catalogTable(schema string, def *hcpDef, key string, fks map[string]string) vgi.CatalogTable {
	full := def.Schema()
	comments := map[string]string{}
	for _, f := range full.Fields() {
		c, _ := f.Metadata.GetValue("comment")
		comments[f.Name] = c
	}
	tags := objectTags(def.Docs)
	tags["domain"] = "infrastructure as code"
	cons := func(id string, kind vgi.ConstraintKind, col string, ref *vgi.ConstraintReference) []byte {
		rec, err := vgi.ConstraintRecord(vgi.ConstraintRecordSpec{SchemaPath: []string{schema}, Table: def.Name, ID: id,
			Kind: kind, Columns: [][]string{{col}}, Enforcement: vgi.ConstraintEnforced, Validation: vgi.ConstraintAllRows,
			References: ref})
		if err != nil {
			panic(err)
		}
		return rec
	}
	constraints := [][]byte{cons(key+"_not_null", vgi.ConstraintNotNull, key, nil), cons("pk", vgi.ConstraintPrimaryKey, key, nil)}
	for col, table := range fks {
		constraints = append(constraints, cons(col+"_fk", vgi.ConstraintForeignKey, col,
			&vgi.ConstraintReference{SchemaPath: []string{schema}, Table: table, Columns: [][]string{{col}}, ConstraintID: "pk"}))
	}
	return vgi.CatalogTable{
		Constraints:    constraints,
		Name:           def.Name,
		Comment:        def.Docs.Description,
		Columns:        full,
		Function:       vgi.AsTableFunction[ScanState](&namedTableFunc{tableFunc{def: def}, schema + "_" + def.Name + "_scan"}),
		ColumnComments: comments,
		Tags:           tags,
	}
}

// namedTableFunc gives a catalog table's scan function its own name:
// vgi-go 0.42.0 resolves a catalog table scan by function name across
// schemas, so unified.runs' scan must not be called "runs" like main.runs.
type namedTableFunc struct {
	tableFunc
	name string
}

func (t *namedTableFunc) Name() string { return t.name }
