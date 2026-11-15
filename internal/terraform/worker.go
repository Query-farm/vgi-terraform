// Package terraform exposes HCP Terraform / Terraform Enterprise and
// Terraform / OpenTofu state and plan files to DuckDB as read-only SQL:
// catalog terraform, schemas main (the API), files (offline readers) and
// unified (the cross-orchestrator run shape).
//
// Run it:
//
//	vgi-terraform-worker                      # stdio: what DuckDB launches by default
//	vgi-terraform-worker --http --port 8000   # HTTP: for Docker / hosted deploys
//
// Attach it from DuckDB:
//
//	CREATE SECRET (TYPE terraform, TOKEN '...', ORGANIZATION 'acme');
//	ATTACH 'terraform' (TYPE vgi, LOCATION '/abs/path/to/bin/vgi-terraform-worker');
package terraform

import (
	"github.com/Query-farm/vgi-go/vgi"
)

// mainTables are the table functions of schema main.
var mainTables = []*hcpDef{
	projectsDef, workspacesDef, runsDef, plansDef, appliesDef, costEstimatesDef,
	policyChecksDef, policyEvaluationsDef, stateVersionsDef, stateVersionOutputsDef, workspaceResourcesDef,
	variablesDef, variableSetsDef, varsetVariablesDef, teamsDef, teamAccessDef, agentPoolsDef, agentsDef,
	registryModulesDef, registryProvidersDef, assessmentsDef, notificationsDef,
}

func examplesOf(defs []*hcpDef) []exampleQuery {
	lists := make([][]vgi.CatalogExample, 0, len(defs))
	for _, d := range defs {
		lists = append(lists, d.Docs.Examples)
	}
	return exampleQueries(lists...)
}

func fileExamples() []exampleQuery {
	lists := make([][]vgi.CatalogExample, 0, len(fileDefs))
	for _, d := range fileDefs {
		lists = append(lists, d.Docs.Examples)
	}
	return exampleQueries(lists...)
}

// NewWorker builds the worker. Extra options (logging, storage) are appended
// after the catalog options.
func NewWorker(opts ...vgi.WorkerOption) *vgi.Worker {
	sourceURL, version := SourceURL, Version
	base := []vgi.WorkerOption{
		vgi.WithCatalogName(CatalogName),
		vgi.WithCatalogComment(CatalogComment),
		vgi.WithCatalogTags(CatalogTags),
		vgi.WithCatalogInfo(vgi.CatalogInfo{SourceURL: &sourceURL, ImplementationVersion: &version}),
		vgi.WithSchemaComments(map[string]string{"main": MainSchemaComment, "files": FilesSchemaComment, "unified": UnifiedSchemaComment}),
		vgi.WithSchemaTags(map[string]map[string]string{
			"main":    schemaTags(mainSchema, examplesOf(append([]*hcpDef{organizationsDef}, mainTables...))),
			"files":   schemaTags(filesSchema, fileExamples()),
			"unified": schemaTags(unifiedSchema, examplesOf([]*hcpDef{unifiedRunsDef, unifiedTaskRunsDef})),
		}),
		vgi.WithSecretTypes(TerraformSecretType),
	}
	w := vgi.NewWorker(append(base, opts...)...)
	for _, d := range mainTables {
		w.RegisterTable(newTable(d))
	}
	w.RegisterCatalogTable("main", catalogTable("main", organizationsDef, "name", nil))
	for _, d := range fileDefs {
		w.RegisterTableInSchema("files", newFileTable(d))
	}
	w.RegisterCatalogTable("unified", catalogTable("unified", unifiedRunsDef, "run_id", nil))
	w.RegisterCatalogTable("unified", catalogTable("unified", unifiedTaskRunsDef, "task_run_id", map[string]string{"run_id": "runs"}))
	return w
}
