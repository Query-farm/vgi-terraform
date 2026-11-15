// Definitions (docs and executable examples) of the offline readers in
// schema files.

package terraform

import "github.com/Query-farm/vgi-go/vgi"

var readStateDef = &fileDef{
	Name:   "read_state",
	Schema: stateSchema,
	Rows:   stateRows,
	Docs: fnDocs{
		Title: "Read State File", Category: "files",
		Keywords:    "terraform.tfstate, state file, opentofu state, resources, instances, attributes, inventory",
		Description: "Resource instances of a terraform.tfstate (v4) file with their attributes; sensitive attributes are masked.",
		DocLLM: "Reads a Terraform / OpenTofu state file (format version 4, written by Terraform 0.12+ and OpenTofu) " +
			"from the worker's filesystem: one row per resource instance (address, type, name, count/for_each key, " +
			"provider, attributes as JSON, dependencies, tainted flag). path is positional; sample:basic.tfstate is a " +
			"bundled example. Every attribute the state lists in sensitive_attributes is replaced by \"(sensitive " +
			"value)\" and its path is listed in the sensitive_attributes column. Extract attribute values with " +
			"attributes->>'id'. No credentials needed. Outputs are in state_outputs(path).",
		DocMD: "## read_state(path)\n\nOne row per resource instance in a `terraform.tfstate` (v4) file: address, " +
			"module, mode, type, name, index key, provider, attributes (JSON, sensitive values masked), dependencies, " +
			"tainted and deposed flags.\n\n`sample:basic.tfstate` is bundled for trying it out.",
		Examples: []vgi.CatalogExample{
			ex("Resource types in a state file", "SELECT type, count(*) AS instances FROM terraform.files.read_state('sample:basic.tfstate') GROUP BY type ORDER BY type"),
			ex("Instances that hold sensitive attributes", "SELECT address, sensitive_attributes FROM terraform.files.read_state('/path/to/terraform.tfstate') WHERE len(sensitive_attributes) > 0 ORDER BY address"),
		},
	},
	Executable: []map[string]any{
		{
			"description":     "Count instances per resource type in the bundled state",
			"sql":             "SELECT type, count(*) AS instances FROM terraform.files.read_state('sample:basic.tfstate') GROUP BY type ORDER BY type",
			"expected_result": [][]any{{"local_file", 1}, {"null_resource", 1}, {"random_id", 1}, {"random_integer", 2}, {"random_password", 1}, {"random_pet", 1}, {"terraform_data", 1}},
		},
		{
			"description":     "Sensitive attributes are masked",
			"sql":             "SELECT address, attributes->>'result' AS result FROM terraform.files.read_state('sample:basic.tfstate') WHERE type = 'random_password'",
			"expected_result": [][]any{{"random_password.db", "(sensitive value)"}},
		},
	},
}

var stateOutputsDef = &fileDef{
	Name:   "state_outputs",
	Schema: stateOutputsSchema,
	Rows:   stateOutputRows,
	Docs: fnDocs{
		Title: "Read State Outputs", Category: "files",
		Keywords:    "terraform outputs, state outputs, tfstate outputs, sensitive outputs",
		Description: "Root module outputs of a terraform.tfstate (v4) file; sensitive outputs have a NULL value.",
		DocLLM: "Reads the outputs of a Terraform / OpenTofu state file (path positional, or sample:basic.tfstate): " +
			"one row per output with its value and type as JSON. value is NULL whenever sensitive is true.",
		DocMD: "## state_outputs(path)\n\nOutputs of a state file: name, sensitive flag, value (JSON, NULL when sensitive) and type.",
		Examples: []vgi.CatalogExample{
			ex("Non-sensitive outputs", "SELECT name, value FROM terraform.files.state_outputs('sample:basic.tfstate') WHERE NOT sensitive ORDER BY name"),
		},
	},
	Executable: []map[string]any{
		{
			"description":     "Outputs of the bundled state, sensitive ones masked",
			"sql":             "SELECT name, sensitive, value IS NULL AS masked FROM terraform.files.state_outputs('sample:basic.tfstate') ORDER BY name",
			"expected_result": [][]any{{"db_password", true, true}, {"naming", false, false}, {"ports", false, false}, {"server_name", false, false}},
		},
	},
}

var readPlanDef = &fileDef{
	Name:   "read_plan",
	Schema: planSchema,
	Rows:   planRows,
	Docs: fnDocs{
		Title: "Read Plan JSON", Category: "files",
		Keywords:    "terraform plan, plan json, terraform show -json, resource changes, replace, forces replacement, destroy, opentofu plan",
		Description: "Resource changes of a `terraform show -json` plan: action, why, which attributes force replacement, and masked before/after values.",
		DocLLM: "Reads the JSON form of a saved plan (the output of `terraform show -json plan.bin` or `tofu show " +
			"-json plan.bin`; path positional, or sample:change.plan.json): one row per resource instance with action " +
			"(create, update, delete, replace, read, no-op), the raw actions list, action_reason, replace_paths (the " +
			"attributes that force a replacement), changed_attributes (top-level attributes that change or become " +
			"unknown), and before / after / after_unknown as JSON. Sensitive values (before_sensitive / " +
			"after_sensitive) are replaced by \"(sensitive value)\". Typical questions: which resources will be " +
			"destroyed or replaced, and which attribute forces each replacement. Output changes are in plan_outputs(path).",
		DocMD: "## read_plan(path)\n\nOne row per resource change of a `terraform show -json` plan: action, " +
			"action reason, `replace_paths` (what forces a replacement), changed attributes and masked before/after " +
			"values.\n\n`sample:change.plan.json` is bundled: an update, two replacements, a create and a delete.",
		Examples: []vgi.CatalogExample{
			ex("What forces each replacement", "SELECT address, action_reason, replace_paths FROM terraform.files.read_plan('sample:change.plan.json') WHERE action = 'replace' ORDER BY address"),
			ex("Count planned actions", "SELECT action, count(*) AS resources FROM terraform.files.read_plan('sample:change.plan.json') GROUP BY action ORDER BY action"),
		},
	},
	Executable: []map[string]any{
		{
			"description":     "Count planned actions in the bundled plan",
			"sql":             "SELECT action, count(*) AS resources FROM terraform.files.read_plan('sample:change.plan.json') GROUP BY action ORDER BY action",
			"expected_result": [][]any{{"create", 1}, {"delete", 1}, {"no-op", 4}, {"replace", 2}, {"update", 1}},
		},
		{
			"description":     "Which attribute forces each replacement",
			"sql":             "SELECT address, replace_paths[1] AS forced_by FROM terraform.files.read_plan('sample:change.plan.json') WHERE action = 'replace' ORDER BY address",
			"expected_result": [][]any{{"local_file.inventory", "content"}, {"random_pet.server", "keepers"}},
		},
	},
}

var planOutputsDef = &fileDef{
	Name:   "plan_outputs",
	Schema: planOutputsSchema,
	Rows:   planOutputRows,
	Docs: fnDocs{
		Title: "Read Plan Outputs", Category: "files",
		Keywords:    "plan outputs, output changes, terraform show -json",
		Description: "Output changes of a `terraform show -json` plan; sensitive values are NULL.",
		DocLLM: "Reads output_changes of a JSON plan (path positional, or sample:change.plan.json): one row per root " +
			"output with its action, sensitive and unknown flags, and before / after values as JSON (NULL when " +
			"sensitive, and after is NULL when unknown until apply).",
		DocMD: "## plan_outputs(path)\n\nOutput changes of a plan: action, sensitive / unknown flags and before/after values.",
		Examples: []vgi.CatalogExample{
			ex("Outputs whose value is only known after apply", "SELECT name, action FROM terraform.files.plan_outputs('sample:change.plan.json') WHERE unknown ORDER BY name"),
		},
	},
	Executable: []map[string]any{
		{
			"description":     "Outputs that change in the bundled plan",
			"sql":             "SELECT name, action, unknown FROM terraform.files.plan_outputs('sample:change.plan.json') WHERE action <> 'no-op' ORDER BY name",
			"expected_result": [][]any{{"server_name", "update", true}},
		},
	},
}

var fileDefs = []*fileDef{readStateDef, stateOutputsDef, readPlanDef, planOutputsDef}
