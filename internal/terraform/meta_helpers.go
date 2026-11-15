// Discovery metadata: catalog and schema descriptions, categories, and the
// helpers every function uses to publish its docs.
//
// DuckDB, coding agents and the vgi-lint metadata checker all read these tags.

package terraform

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Query-farm/vgi-go/vgi"
	"github.com/apache/arrow-go/v18/arrow"
)

// CatalogName is the name DuckDB attaches the worker under.
const CatalogName = "terraform"

// SourceURL is where this worker's code lives.
const SourceURL = "https://github.com/Query-farm/vgi-terraform"

// category is one entry of the schema's vgi.categories list.
type category struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// SchemaCategories group functions in docs and agent tools. Every function's
// vgi.category must name one of these.
var SchemaCategories = []category{
	{Name: "organization", Title: "Organization", Description: "Organizations, projects, teams, team access, agent pools and agents of HCP Terraform / Terraform Enterprise."},
	{Name: "workspaces", Title: "Workspaces", Description: "Workspaces, their variables, variable sets, notification configurations and drift-detection health assessments."},
	{Name: "runs", Title: "Runs", Description: "Runs and their plans, applies, cost estimates, Sentinel policy checks and OPA/Sentinel policy evaluations."},
	{Name: "state", Title: "State", Description: "State versions, state version outputs and the resources tracked in a workspace's current state."},
	{Name: "registry", Title: "Private Registry", Description: "Modules and providers published in an organization's private registry."},
	{Name: "files", Title: "State and Plan Files", Description: "Offline readers for terraform.tfstate files and `terraform show -json` plan output; no credentials needed."},
	{Name: "unified", Title: "Unified Orchestrator Shape", Description: "runs / task_runs with the columns shared by every orchestrator worker."},
}

// mustJSON marshals v, panicking on failure (only static data is passed).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// keywordsArray turns "a, b, c" into the JSON array string vgi.keywords requires.
func keywordsArray(keywords string) string {
	out := []string{}
	for _, k := range strings.Split(keywords, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return mustJSON(out)
}

// fnDocs is the discovery text of one function or table.
type fnDocs struct {
	Title       string
	Category    string
	Keywords    string
	Description string
	DocLLM      string
	DocMD       string
	Examples    []vgi.CatalogExample
}

// objectTags builds the standard per-object discovery tags.
func objectTags(d fnDocs) map[string]string {
	known := false
	for _, c := range SchemaCategories {
		known = known || c.Name == d.Category
	}
	if !known {
		panic(fmt.Sprintf("unknown category %q; add it to SchemaCategories", d.Category))
	}
	return withExamples(map[string]string{
		"vgi.title":    d.Title,
		"vgi.category": d.Category,
		"vgi.keywords": keywordsArray(d.Keywords),
		"vgi.doc_llm":  d.DocLLM,
		"vgi.doc_md":   d.DocMD,
	}, d.Examples)
}

// resultColumn describes one output column for vgi.result_columns_schema.
type resultColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// column builds an Arrow field whose comment shows up in DuckDB's DESCRIBE and agent tools.
func column(name string, typ arrow.DataType, comment string, nullable bool) arrow.Field {
	return arrow.Field{Name: name, Type: typ, Nullable: nullable,
		Metadata: arrow.NewMetadata([]string{"comment"}, []string{comment})}
}

// duckType names an Arrow type the way DuckDB shows it.
func duckType(t arrow.DataType) string {
	switch x := t.(type) {
	case *arrow.StringType:
		return "VARCHAR"
	case *arrow.Int64Type:
		return "BIGINT"
	case *arrow.Int32Type:
		return "INTEGER"
	case *arrow.Float64Type:
		return "DOUBLE"
	case *arrow.BooleanType:
		return "BOOLEAN"
	case *arrow.TimestampType:
		return "TIMESTAMP WITH TIME ZONE"
	case *arrow.ListType:
		return duckType(x.Elem()) + "[]"
	case *arrow.StructType:
		parts := make([]string, x.NumFields())
		for i, f := range x.Fields() {
			parts[i] = f.Name + " " + duckType(f.Type)
		}
		return "STRUCT(" + strings.Join(parts, ", ") + ")"
	case arrow.ExtensionType:
		return "JSON"
	}
	return t.String()
}

// schemaColumns lists a schema's columns for vgi.result_columns_schema.
func schemaColumns(s *arrow.Schema) []resultColumn {
	out := make([]resultColumn, s.NumFields())
	for i, f := range s.Fields() {
		c, _ := f.Metadata.GetValue("comment")
		out[i] = resultColumn{Name: f.Name, Type: duckType(f.Type), Description: c}
	}
	return out
}

// exampleQuery is one entry of vgi.example_queries.
type exampleQuery struct {
	Description string `json:"description"`
	SQL         string `json:"sql"`
}

// exampleQueries converts catalog examples to vgi.example_queries entries.
func exampleQueries(lists ...[]vgi.CatalogExample) []exampleQuery {
	out := []exampleQuery{}
	for _, examples := range lists {
		for _, ex := range examples {
			out = append(out, exampleQuery{Description: ex.Description, SQL: ex.SQL})
		}
	}
	return out
}

// withExamples publishes examples (with descriptions) as vgi.example_queries.
func withExamples(tags map[string]string, lists ...[]vgi.CatalogExample) map[string]string {
	tags["vgi.example_queries"] = mustJSON(exampleQueries(lists...))
	return tags
}
