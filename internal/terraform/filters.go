// Exact application of pushed-down filters.
//
// DuckDB removes every predicate it pushes into a table function from its own
// plan, so the worker must return exactly the rows that satisfy it. The HCP
// Terraform query parameters (pushdown.go) only narrow what the API returns;
// every pushed predicate is re-applied here:
//
//   - comparisons, IN, IS [NOT] NULL and AND/OR trees of them over VARCHAR,
//     integer, DOUBLE, BOOLEAN and TIMESTAMPTZ columns are evaluated in Go with
//     SQL three-valued logic;
//   - anything else (LIKE, functions, list/JSON columns) falls back to the
//     SDK's embedded DuckDB evaluator (vgi/exprduckdb) over the candidate rows.

package terraform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Query-farm/vgi-go/vgi"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/scalar"

	// The DuckDB expression evaluator for filters Go cannot evaluate natively.
	_ "github.com/Query-farm/vgi-go/vgi/exprduckdb"
)

type tri int8

const (
	triFalse tri = iota
	triTrue
	triNull
)

// scalarGo converts a filter literal to the Go value rows hold. ok is false
// for an unsupported literal type; a NULL literal is (nil, true).
func scalarGo(s scalar.Scalar) (any, bool) {
	if s == nil {
		return nil, false
	}
	if !s.IsValid() {
		return nil, true
	}
	switch v := s.(type) {
	case *scalar.String:
		return string(v.Value.Bytes()), true
	case *scalar.LargeString:
		return string(v.Value.Bytes()), true
	case *scalar.Int8:
		return int64(v.Value), true
	case *scalar.Int16:
		return int64(v.Value), true
	case *scalar.Int32:
		return int64(v.Value), true
	case *scalar.Int64:
		return v.Value, true
	case *scalar.Uint8:
		return int64(v.Value), true
	case *scalar.Uint16:
		return int64(v.Value), true
	case *scalar.Uint32:
		return int64(v.Value), true
	case *scalar.Float32:
		return float64(v.Value), true
	case *scalar.Float64:
		return v.Value, true
	case *scalar.Boolean:
		return v.Value, true
	case *scalar.Timestamp:
		return v.Value.ToTime(v.Type.(*arrow.TimestampType).Unit).UTC(), true
	case *scalar.Date32:
		return v.Value.ToTime().UTC(), true
	}
	return nil, false
}

// compareGo orders two values of the same kind; ok is false for mixed or
// unsupported kinds.
func compareGo(a, b any) (int, bool) {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case int64:
		switch y := b.(type) {
		case int64:
			return cmpOrdered(x, y), true
		case float64:
			return cmpOrdered(float64(x), y), true
		}
	case float64:
		switch y := b.(type) {
		case float64:
			return cmpOrdered(x, y), true
		case int64:
			return cmpOrdered(x, float64(y)), true
		}
	case bool:
		y, ok := b.(bool)
		if !ok {
			return 0, false
		}
		return cmpOrdered(boolInt(x), boolInt(y)), true
	case time.Time:
		y, ok := b.(time.Time)
		if !ok {
			return 0, false
		}
		return x.Compare(y), true
	}
	return 0, false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func cmpOrdered[T int | int64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// scalarKind reports whether a row value is one the native evaluator handles.
func scalarKind(v any) bool {
	switch v.(type) {
	case nil, string, int64, float64, bool, time.Time:
		return true
	}
	return false
}

// evalNative evaluates a filter on one row; ok is false when the shape or a
// type is not handled natively.
func evalNative(f vgi.Filter, row []any, idx map[string]int) (tri, bool) {
	col := func(name string) (any, bool) {
		i, ok := idx[name]
		if !ok || !scalarKind(row[i]) {
			return nil, false
		}
		return row[i], true
	}
	switch ft := f.(type) {
	case *vgi.ConstantFilter:
		v, ok := col(ft.ColumnName())
		if !ok {
			return triNull, false
		}
		lit, ok := scalarGo(ft.Value)
		if !ok {
			return triNull, false
		}
		if v == nil || lit == nil {
			return triNull, true
		}
		c, ok := compareGo(v, lit)
		if !ok {
			return triNull, false
		}
		var r bool
		switch ft.Op {
		case vgi.OpEQ:
			r = c == 0
		case vgi.OpNE:
			r = c != 0
		case vgi.OpLT:
			r = c < 0
		case vgi.OpLE:
			r = c <= 0
		case vgi.OpGT:
			r = c > 0
		case vgi.OpGE:
			r = c >= 0
		default:
			return triNull, false
		}
		if r {
			return triTrue, true
		}
		return triFalse, true
	case *vgi.IsNullFilter:
		i, ok := idx[ft.ColumnName()]
		if !ok {
			return triNull, false
		}
		if row[i] == nil {
			return triTrue, true
		}
		return triFalse, true
	case *vgi.IsNotNullFilter:
		i, ok := idx[ft.ColumnName()]
		if !ok {
			return triNull, false
		}
		if row[i] != nil {
			return triTrue, true
		}
		return triFalse, true
	case *vgi.InFilter:
		v, ok := col(ft.ColumnName())
		if !ok || ft.Values == nil {
			return triNull, false
		}
		if v == nil {
			return triNull, true
		}
		sawNull := false
		for i := 0; i < ft.Values.Len(); i++ {
			s, err := scalar.GetScalar(ft.Values, i)
			if err != nil {
				return triNull, false
			}
			lit, ok := scalarGo(s)
			if !ok {
				return triNull, false
			}
			if lit == nil {
				sawNull = true
				continue
			}
			c, ok := compareGo(v, lit)
			if !ok {
				return triNull, false
			}
			if c == 0 {
				return triTrue, true
			}
		}
		if sawNull {
			return triNull, true
		}
		return triFalse, true
	case *vgi.AndFilter:
		out := triTrue
		for _, ch := range ft.Children {
			r, ok := evalNative(ch, row, idx)
			if !ok {
				return triNull, false
			}
			if r == triFalse {
				out = triFalse
			} else if r == triNull && out == triTrue {
				out = triNull
			}
		}
		return out, true
	case *vgi.OrFilter:
		out := triFalse
		for _, ch := range ft.Children {
			r, ok := evalNative(ch, row, idx)
			if !ok {
				return triNull, false
			}
			if r == triTrue {
				out = triTrue
			} else if r == triNull && out == triFalse {
				out = triNull
			}
		}
		return out, true
	}
	return triNull, false
}

// applyFilters keeps the rows (full-width, in full's column order) that pass
// every pushed filter.
func applyFilters(ctx context.Context, pf *vgi.PushdownFilters, full *arrow.Schema, rows [][]any) ([][]any, error) {
	if pf == nil || len(pf.Filters) == 0 || len(rows) == 0 {
		return rows, nil
	}
	idx := make(map[string]int, full.NumFields())
	for i, f := range full.Fields() {
		idx[f.Name] = i
	}
	keep := make([]bool, len(rows))
	for i := range keep {
		keep[i] = true
	}
	for _, f := range pf.Filters {
		native := true
		results := make([]bool, len(rows))
		for i, r := range rows {
			if !keep[i] {
				continue
			}
			res, ok := evalNative(f, r, idx)
			if !ok {
				native = false
				break
			}
			results[i] = res == triTrue
		}
		if native {
			for i := range keep {
				keep[i] = keep[i] && results[i]
			}
			continue
		}
		// Fallback: the SDK's DuckDB evaluator over the candidate rows.
		var cand [][]any
		var pos []int
		for i, r := range rows {
			if keep[i] {
				cand = append(cand, r)
				pos = append(pos, i)
			}
		}
		if len(cand) == 0 {
			break
		}
		staging := stagingSchema(full)
		batch, err := buildBatch(staging, staging, cand)
		if err != nil {
			return nil, err
		}
		mask, err := f.Evaluate(ctx, batch)
		batch.Release()
		if err != nil {
			if strings.Contains(err.Error(), "runtime filter has no evaluator") {
				// Dynamic (join / Top-N) filters are advisory: skipping one
				// only returns rows DuckDB discards itself.
				continue
			}
			return nil, fmt.Errorf("applying pushed-down filter on %q: %w (expressions over JSON and "+
				"TIMESTAMPTZ columns are evaluated on their text; compare those columns with =, <, > or IN instead)", f.ColumnName(), err)
		}
		b, ok := mask.(*array.Boolean)
		if !ok || b.Len() != len(cand) {
			mask.Release()
			return nil, fmt.Errorf("filter on %q did not evaluate to one boolean per row", f.ColumnName())
		}
		for j, p := range pos {
			keep[p] = b.IsValid(j) && b.Value(j)
		}
		mask.Release()
	}
	out := make([][]any, 0, len(rows))
	for i, r := range rows {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out, nil
}

// stagingSchema is full with JSON and TIMESTAMPTZ columns as plain VARCHAR:
// the embedded DuckDB evaluator in vgi-go 0.42.0 supports neither type, even
// in a batch where the expression does not reference them.
func stagingSchema(full *arrow.Schema) *arrow.Schema {
	fields := make([]arrow.Field, full.NumFields())
	for i, f := range full.Fields() {
		switch f.Type.(type) {
		case arrow.ExtensionType, *arrow.TimestampType:
			f.Type = arrow.BinaryTypes.String
		}
		fields[i] = f
	}
	return arrow.NewSchema(fields, nil)
}
