// The table-function framework every HCP Terraform table uses.
//
// An hcpDef names its arguments, columns and docs, and supplies Start (once
// per scan: the first API request(s) to make) and optionally Handle (turn one
// response into rows, and possibly queue follow-up requests). The wrapper
// connects with the session's terraform secret, issues one paginated request
// per step, re-applies pushed filters exactly, projects the requested columns
// and streams one API page per batch.
//
// The cursor lives in ScanState, whose exported fields the SDK gob-encodes
// into HTTP continuation tokens, so a long scan survives the HTTP transport
// and resumes at the page it stopped on. It never holds credentials: the
// secret is re-read on every call.

package terraform

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/Query-farm/vgi-go/vgi"
	"github.com/Query-farm/vgi-rpc-go/vgirpc"
	"github.com/apache/arrow-go/v18/arrow"
)

// pageSize is page[size] for every list request (the API maximum).
const pageSize = 100

// Step is one API request sequence: a path, its query string and the parent
// values (run id, workspace name, ...) its rows inherit.
type Step struct {
	Path  string
	Query string
	Paged bool
	Kind  string
	Ctx   map[string]string
}

// ScanState is the resumable cursor shared by every table.
type ScanState struct {
	Started bool
	Done    bool
	Steps   []Step
	Idx     int
	Page    int
	// MinCreatedMicros (when HasMin) stops a newest-first run listing once
	// it passes below a pushed-down created_at / queued_at lower bound.
	MinCreatedMicros int64
	HasMin           bool
}

// rowCtx is what a column getter can see besides the resource itself.
type rowCtx struct {
	c    *client
	doc  *document
	step *Step
}

// colDef is one output column and how to read it from a resource.
type colDef struct {
	F   arrow.Field
	Get func(r *resource, x *rowCtx) any
}

// hcpDef defines one table.
type hcpDef struct {
	Name string
	Docs fnDocs
	// Args is a struct with `vgi:"..."` tags (nil for no arguments).
	Args any
	Cols []colDef
	// Filter enables filter pushdown (filters are always re-applied exactly).
	Filter bool
	Start  func(ctx context.Context, c *client, p *vgi.ProcessParams, st *ScanState) error
	// Handle turns one response into rows; nil maps every resource through Cols.
	Handle func(x *rowCtx, res []resource, st *ScanState) (rows [][]any, more []Step, stop bool)

	schema *arrow.Schema
}

// Schema is the output schema built from Cols.
func (d *hcpDef) Schema() *arrow.Schema {
	if d.schema == nil {
		fields := make([]arrow.Field, len(d.Cols))
		for i, c := range d.Cols {
			fields[i] = c.F
		}
		d.schema = arrow.NewSchema(fields, nil)
	}
	return d.schema
}

// row maps one resource through Cols.
func (d *hcpDef) row(r *resource, x *rowCtx) []any {
	out := make([]any, len(d.Cols))
	for i, c := range d.Cols {
		if c.Get != nil {
			out[i] = c.Get(r, x)
		}
	}
	return out
}

type tableFunc struct{ def *hcpDef }

var _ vgi.TypedTableFunc[ScanState] = (*tableFunc)(nil)

func (t *tableFunc) Name() string { return t.def.Name }

func (t *tableFunc) Metadata() vgi.FunctionMetadata {
	d := t.def.Docs
	tags := objectTags(d)
	tags["vgi.result_columns_schema"] = mustJSON(schemaColumns(t.def.Schema()))
	return vgi.FunctionMetadata{
		Description:        d.Description,
		Stability:          vgi.StabilityVolatile,
		ProjectionPushdown: true,
		FilterPushdown:     t.def.Filter,
		RequiredSecrets:    []vgi.SecretRequirement{{SecretType: SecretType}},
		Tags:               tags,
		Examples:           d.Examples,
	}
}

func (t *tableFunc) ArgumentSpecs() []vgi.ArgSpec {
	if t.def.Args == nil {
		return nil
	}
	return vgi.DeriveArgSpecs(t.def.Args)
}

func (t *tableFunc) OnBind(*vgi.BindParams) (*vgi.BindResponse, error) {
	return vgi.BindSchema(t.def.Schema())
}

func (t *tableFunc) NewState(*vgi.ProcessParams) (*ScanState, error) {
	return &ScanState{}, nil
}

func (t *tableFunc) Process(ctx context.Context, p *vgi.ProcessParams, st *ScanState, out *vgirpc.OutputCollector) error {
	if st.Done {
		return out.Finish()
	}
	c, err := connect(p.Secrets)
	if err != nil {
		return err
	}
	if !st.Started {
		if err := t.def.Start(ctx, c, p, st); err != nil {
			return err
		}
		st.Started, st.Page = true, 1
	}
	full := t.def.Schema()
	for {
		if st.Idx >= len(st.Steps) {
			st.Done = true
			return out.Finish()
		}
		step := &st.Steps[st.Idx]
		q, err := url.ParseQuery(step.Query)
		if err != nil {
			return err
		}
		if step.Paged {
			q.Set("page[number]", strconv.Itoa(st.Page))
			q.Set("page[size]", strconv.Itoa(pageSize))
		}
		doc, err := c.get(ctx, step.Path, q)
		if err != nil {
			return err
		}
		res, err := doc.list()
		if err != nil {
			return err
		}
		x := &rowCtx{c: c, doc: doc, step: step}
		var rows [][]any
		var more []Step
		stop := false
		if t.def.Handle != nil {
			rows, more, stop = t.def.Handle(x, res, st)
		} else {
			for i := range res {
				rows = append(rows, t.def.row(&res[i], x))
			}
		}
		st.Steps = append(st.Steps, more...)
		if next := doc.nextPage(); step.Paged && next > st.Page && !stop {
			st.Page = next
		} else {
			st.Idx++
			st.Page = 1
		}
		if t.def.Filter {
			rows, err = applyFilters(ctx, p.CurrentPushdownFilters, full, rows)
			if err != nil {
				return err
			}
		}
		if len(rows) > 0 {
			batch, err := buildBatch(p.OutputSchema, full, rows)
			if err != nil {
				return err
			}
			return out.Emit(batch)
		}
	}
}

// newTable wraps a definition for registration.
func newTable(def *hcpDef) vgi.TableFunction {
	return vgi.AsTableFunction[ScanState](&tableFunc{def: def})
}

// single queues one paged request.
func single(st *ScanState, path string, q url.Values, ctxVals map[string]string) {
	st.Steps = append(st.Steps, Step{Path: path, Query: q.Encode(), Paged: true, Ctx: ctxVals})
}

// ---- column constructors ---------------------------------------------------

func cID(name, doc string) colDef {
	return colDef{column(name, tString, doc, false), func(r *resource, _ *rowCtx) any { return r.ID }}
}

func cStr(name, attr, doc string) colDef {
	return colDef{column(name, tString, doc, true), func(r *resource, _ *rowCtx) any { return r.str(attr) }}
}

func cInt(name, attr, doc string) colDef {
	return colDef{column(name, tInt64, doc, true), func(r *resource, _ *rowCtx) any { return r.i64(attr) }}
}

func cFloat(name, attr, doc string) colDef {
	return colDef{column(name, tFloat64, doc, true), func(r *resource, _ *rowCtx) any { return r.f64(attr) }}
}

func cBool(name, attr, doc string) colDef {
	return colDef{column(name, tBool, doc, true), func(r *resource, _ *rowCtx) any { return r.boolean(attr) }}
}

func cTS(name, attr, doc string) colDef {
	return colDef{column(name, tTS, doc, true), func(r *resource, _ *rowCtx) any { return r.ts(attr) }}
}

func cList(name, attr, doc string) colDef {
	return colDef{column(name, tStrList, doc, true), func(r *resource, _ *rowCtx) any { return r.strList(attr) }}
}

func cJSON(name, attr, doc string) colDef {
	return colDef{column(name, tJSON, doc, true), func(r *resource, _ *rowCtx) any { return r.jsonAttr(attr) }}
}

func cRel(name, rel, doc string) colDef {
	return colDef{column(name, tString, doc, true), func(r *resource, _ *rowCtx) any { return nilIfEmpty(r.relID(rel)) }}
}

// cParent reads a value the step inherited from its parent request.
func cParent(name, key, doc string, nullable bool) colDef {
	return colDef{column(name, tString, doc, nullable), func(_ *resource, x *rowCtx) any { return nilIfEmpty(x.step.Ctx[key]) }}
}

// cSecondsFromMillis converts a millisecond attribute to DOUBLE seconds.
func cSecondsFromMillis(name, attr, doc string) colDef {
	return colDef{column(name, tFloat64, doc, true), func(r *resource, _ *rowCtx) any {
		if v, ok := r.f64(attr).(float64); ok {
			return v / 1000
		}
		return nil
	}}
}

// microsOf converts a timestamp row value to Unix microseconds.
func microsOf(v any) (int64, bool) {
	t, ok := v.(time.Time)
	if !ok {
		return 0, false
	}
	return t.UnixMicro(), true
}
