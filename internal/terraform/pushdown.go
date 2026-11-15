// Translating pushed-down WHERE filters into HCP Terraform query parameters.
//
// Only top-level conjuncts are used, and only ones the API can express:
// equality / IN on a VARCHAR column becomes a filter[...] / search[...]
// parameter, and a lower bound on a run's creation time stops a newest-first
// run listing early. Every translation may only widen what the API returns;
// filters.go re-applies each predicate exactly, so a filter the API ignores
// or matches fuzzily (search[name]) is still correct.

package terraform

import (
	"time"

	"github.com/Query-farm/vgi-go/vgi"
	"github.com/apache/arrow-go/v18/arrow/scalar"
)

// conjuncts flattens the top-level AND of the pushed filters.
func conjuncts(pf *vgi.PushdownFilters) []vgi.Filter {
	if pf == nil {
		return nil
	}
	var out []vgi.Filter
	var walk func(f vgi.Filter)
	walk = func(f vgi.Filter) {
		if a, ok := f.(*vgi.AndFilter); ok {
			for _, c := range a.Children {
				walk(c)
			}
			return
		}
		out = append(out, f)
	}
	for _, f := range pf.Filters {
		walk(f)
	}
	return out
}

// eqStrings returns the values a column must equal (col = 'x' or col IN
// (...)), from the first such conjunct.
func eqStrings(pf *vgi.PushdownFilters, col string) ([]string, bool) {
	for _, f := range conjuncts(pf) {
		if f.ColumnName() != col {
			continue
		}
		switch x := f.(type) {
		case *vgi.ConstantFilter:
			if x.Op != vgi.OpEQ {
				continue
			}
			if v, ok := scalarGo(x.Value); ok {
				if s, ok := v.(string); ok {
					return []string{s}, true
				}
			}
		case *vgi.InFilter:
			if x.Values == nil {
				continue
			}
			var vals []string
			ok := true
			for i := 0; i < x.Values.Len(); i++ {
				s, err := scalar.GetScalar(x.Values, i)
				if err != nil {
					ok = false
					break
				}
				v, good := scalarGo(s)
				str, isStr := v.(string)
				if !good || !isStr {
					ok = false
					break
				}
				vals = append(vals, str)
			}
			if ok && len(vals) > 0 {
				return vals, true
			}
		}
	}
	return nil, false
}

// lowerBound is the tightest col >= t / col > t bound among the conjuncts.
func lowerBound(pf *vgi.PushdownFilters, col string) (time.Time, bool) {
	var best time.Time
	found := false
	for _, f := range conjuncts(pf) {
		x, ok := f.(*vgi.ConstantFilter)
		if !ok || x.ColumnName() != col || (x.Op != vgi.OpGE && x.Op != vgi.OpGT) {
			continue
		}
		v, ok := scalarGo(x.Value)
		if !ok {
			continue
		}
		t, ok := v.(time.Time)
		if !ok {
			continue
		}
		if !found || t.After(best) {
			best, found = t, true
		}
	}
	return best, found
}

// setMin records a creation-time lower bound for early termination.
func setMin(st *ScanState, pf *vgi.PushdownFilters, col string) {
	if t, ok := lowerBound(pf, col); ok {
		st.MinCreatedMicros, st.HasMin = t.UnixMicro(), true
	}
}

// pastMin reports whether a newest-first listing has gone below the bound.
func pastMin(st *ScanState, created any) bool {
	if !st.HasMin {
		return false
	}
	m, ok := microsOf(created)
	return ok && m < st.MinCreatedMicros
}
