// State tables: state versions, state version outputs and the resources of a
// workspace's current state.

package terraform

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/Query-farm/vgi-go/vgi"
)

type stateVersionsArgs struct {
	WorkspaceName string  `vgi:"name=workspace_name,pos=0,doc=Workspace name (not id); the API lists state versions by organization and workspace name."`
	Organization  *string `vgi:"name=organization,doc=Organization name; default the ORGANIZATION of the terraform secret."`
}

var stateVersionsDef = &hcpDef{
	Name: "state_versions",
	Args: stateVersionsArgs{},
	Docs: fnDocs{
		Title: "State Versions", Category: "state",
		Keywords:    "state versions, terraform state, tfstate history, serial, state metadata",
		Description: "Every state version of a workspace, newest first: serial, Terraform version, size and the run that wrote it.",
		DocLLM: "One row per state version of one workspace (workspace_name positional, organization :=). Metadata " +
			"only: the state content is not downloaded. serial increases with each write; run_id is the run that " +
			"created it (NULL for CLI/API uploads). Outputs of a version: state_version_outputs(state_version_id := id).",
		DocMD: "## state_versions(workspace_name, organization := ...)\n\nState version history: serial, Terraform version, size, status, VCS commit and run.",
		Examples: []vgi.CatalogExample{
			ex("State versions written outside of runs", "SELECT id, serial, created_at FROM terraform.main.state_versions('prod-network') WHERE run_id IS NULL ORDER BY serial DESC"),
		},
	},
	Cols: []colDef{
		cID("id", "State version id (sv-...)."),
		cParent("workspace_name", "workspace_name", "Workspace name.", false),
		cInt("serial", "serial", "State serial number."),
		cInt("state_version", "state-version", "State file format version (4)."),
		cStr("terraform_version", "terraform-version", "Terraform version that wrote the state."),
		cStr("status", "status", "pending, finalized or discarded."),
		cInt("size", "size", "State size in bytes."),
		cBool("resources_processed", "resources-processed", "Whether outputs and resources have been extracted."),
		cStr("vcs_commit_sha", "vcs-commit-sha", "VCS commit of the run's configuration."),
		cRel("run_id", "run", "Run that created the version (NULL for uploads)."),
		cTS("created_at", "created-at", "When the version was created."),
	},
	Start: func(_ context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error {
		var a stateVersionsArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		name := strings.TrimSpace(a.WorkspaceName)
		if name == "" {
			return errEmptyArg("workspace_name")
		}
		org, err := c.cfg.org(a.Organization)
		if err != nil {
			return err
		}
		q := url.Values{"filter[workspace][name]": {name}, "filter[organization][name]": {org}}
		single(st, "/state-versions", q, map[string]string{"workspace_name": name, "organization": org})
		return nil
	},
}

type outputsArgs struct {
	StateVersionID *string `vgi:"name=state_version_id,doc=State version id (sv-...) whose outputs to list."`
	WorkspaceID    *string `vgi:"name=workspace_id,doc=Workspace id (ws-...): list the outputs of its current state version instead."`
}

var stateVersionOutputsDef = &hcpDef{
	Name: "state_version_outputs",
	Args: outputsArgs{},
	Docs: fnDocs{
		Title: "State Version Outputs", Category: "state",
		Keywords:    "outputs, terraform outputs, state outputs, sensitive outputs",
		Description: "Outputs of a state version, or of a workspace's current state; sensitive values are always NULL.",
		DocLLM: "One row per output. Pass exactly one of state_version_id := 'sv-...' or workspace_id := 'ws-...' " +
			"(the workspace's current state). value is the output value as JSON, NULL whenever sensitive is true.",
		DocMD: "## state_version_outputs(state_version_id := ... | workspace_id := ...)\n\nOutputs: name, type, sensitive flag and value (NULL when sensitive).",
		Examples: []vgi.CatalogExample{
			ex("Current outputs of a workspace", "SELECT name, value FROM terraform.main.state_version_outputs(workspace_id := 'ws-abc123') WHERE NOT sensitive ORDER BY name"),
		},
	},
	Cols: []colDef{
		cID("id", "Output id (wsout-...)."),
		cParent("state_version_id", "state_version_id", "State version id (NULL when read through workspace_id).", true),
		cParent("workspace_id", "workspace_id", "Workspace id (NULL when read through state_version_id).", true),
		cStr("name", "name", "Output name."),
		cBool("sensitive", "sensitive", "Whether the output is sensitive."),
		cStr("type", "type", "Value type, e.g. string, array, object."),
		{column("detailed_type", tJSON, "Terraform type constraint as JSON, e.g. [\"list\",\"string\"].", true), func(r *resource, _ *rowCtx) any {
			if v := r.jsonAttr("detailed-type"); v != nil {
				return v
			}
			return r.jsonAttr("detailed_type")
		}},
		{column("value", tJSON, "Value as JSON; NULL when sensitive.", true), func(r *resource, _ *rowCtx) any {
			if s, _ := r.boolean("sensitive").(bool); s {
				return nil
			}
			return r.jsonAttr("value")
		}},
	},
	Start: func(_ context.Context, _ *client, p *vgi.ProcessParams, st *ScanState) error {
		var a outputsArgs
		if err := vgi.BindArgs(p.Args, &a); err != nil {
			return err
		}
		sv, ws := "", ""
		if a.StateVersionID != nil {
			sv = strings.TrimSpace(*a.StateVersionID)
		}
		if a.WorkspaceID != nil {
			ws = strings.TrimSpace(*a.WorkspaceID)
		}
		switch {
		case sv != "" && ws == "":
			single(st, "/state-versions/"+url.PathEscape(sv)+"/outputs", url.Values{}, map[string]string{"state_version_id": sv})
		case ws != "" && sv == "":
			single(st, "/workspaces/"+url.PathEscape(ws)+"/current-state-version-outputs", url.Values{}, map[string]string{"workspace_id": ws})
		default:
			return errors.New("pass exactly one of state_version_id := 'sv-...' or workspace_id := 'ws-...'")
		}
		return nil
	},
}

var workspaceResourcesDef = &hcpDef{
	Name: "workspace_resources",
	Args: workspaceIDArgs{},
	Docs: fnDocs{
		Title: "Workspace Resources", Category: "state",
		Keywords:    "resources, managed resources, inventory, resource address, provider",
		Description: "Resources in a workspace's current state: address, type, module and provider.",
		DocLLM: "One row per resource in the current state of one workspace (workspace_id positional). provider_type " +
			"is the resource type (aws_instance, ...); module is 'root' or the module path. Use it as an " +
			"infrastructure inventory; for attribute values read the state file with terraform.files.read_state().",
		DocMD: "## workspace_resources(workspace_id)\n\nCurrent-state resources: address, name, type, module, provider and the state version that last modified each.",
		Examples: []vgi.CatalogExample{
			ex("Resource types in a workspace", "SELECT provider_type, count(*) AS n FROM terraform.main.workspace_resources('ws-abc123') GROUP BY provider_type ORDER BY n DESC"),
		},
	},
	Cols: []colDef{
		cID("id", "Resource id (wsr-...)."),
		cParent("workspace_id", "workspace_id", "Workspace id.", false),
		cStr("address", "address", "Resource address, e.g. module.vpc.aws_subnet.private."),
		cStr("name", "name", "Resource name."),
		cStr("provider_type", "provider-type", "Resource type, e.g. aws_instance."),
		cStr("provider", "provider", "Provider address, e.g. hashicorp/aws."),
		cStr("module", "module", "Module path, 'root' for the root module."),
		cStr("name_index", "name-index", "count / for_each key, if any."),
		cStr("modified_by_state_version_id", "modified-by-state-version-id", "State version that last changed the resource."),
		cTS("created_at", "created-at", "When the resource was first seen."),
		cTS("updated_at", "updated-at", "When it last changed."),
	},
	Start: workspaceStart(func(id string) (string, url.Values) {
		return "/workspaces/" + url.PathEscape(id) + "/resources", url.Values{}
	}),
}
