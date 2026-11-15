// JSON:API documents and typed attribute accessors.

package terraform

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// resource is one JSON:API resource object.
type resource struct {
	ID            string                  `json:"id"`
	Type          string                  `json:"type"`
	Attributes    map[string]any          `json:"attributes"`
	Relationships map[string]relationship `json:"relationships"`
}

// relationship holds a to-one ({id,type}), to-many ([...]) or null linkage.
type relationship struct {
	Data json.RawMessage `json:"data"`
}

type linkage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// pagination is meta.pagination.
type pagination struct {
	CurrentPage int  `json:"current-page"`
	NextPage    *int `json:"next-page"`
}

// document is one JSON:API response.
type document struct {
	Data     json.RawMessage `json:"data"`
	Included []resource      `json:"included"`
	Meta     struct {
		Pagination *pagination `json:"pagination"`
	} `json:"meta"`
}

// list returns data as a list of resources (a single object becomes one).
func (d *document) list() ([]resource, error) {
	raw := strings.TrimSpace(string(d.Data))
	if raw == "" || raw == "null" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "{") {
		var r resource
		if err := decodeNumber(d.Data, &r); err != nil {
			return nil, err
		}
		return []resource{r}, nil
	}
	var rs []resource
	if err := decodeNumber(d.Data, &rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func decodeNumber(b []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	return dec.Decode(v)
}

// included finds an included resource by type and id.
func (d *document) included(typ, id string) *resource {
	if id == "" {
		return nil
	}
	for i := range d.Included {
		if d.Included[i].ID == id && d.Included[i].Type == typ {
			return &d.Included[i]
		}
	}
	return nil
}

// nextPage is the next page number, or 0 when this is the last page.
func (d *document) nextPage() int {
	if d.Meta.Pagination == nil || d.Meta.Pagination.NextPage == nil {
		return 0
	}
	return *d.Meta.Pagination.NextPage
}

// relID is the id of a to-one relationship ("" when absent or null).
func (r *resource) relID(name string) string {
	if r == nil {
		return ""
	}
	rel, ok := r.Relationships[name]
	if !ok {
		return ""
	}
	var l linkage
	if json.Unmarshal(rel.Data, &l) != nil {
		return ""
	}
	return l.ID
}

// relIDs lists the ids of a to-many relationship.
func (r *resource) relIDs(name string) []string {
	rel, ok := r.Relationships[name]
	if !ok {
		return nil
	}
	var ls []linkage
	if json.Unmarshal(rel.Data, &ls) != nil {
		return nil
	}
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

// attr walks a dotted attribute path ("result.passed").
func (r *resource) attr(path string) any {
	if r == nil {
		return nil
	}
	var cur any = r.Attributes
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// str is a VARCHAR attribute value (nil when absent, null or "").
func (r *resource) str(path string) any {
	switch v := r.attr(path).(type) {
	case string:
		return nilIfEmpty(v)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}
	return nil
}

// i64 is a BIGINT attribute value; numeric strings are accepted.
func (r *resource) i64(path string) any {
	switch v := r.attr(path).(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		if f, err := v.Float64(); err == nil {
			return int64(f)
		}
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return nil
}

// f64 is a DOUBLE attribute value; numeric strings ("12.30") are accepted.
func (r *resource) f64(path string) any {
	switch v := r.attr(path).(type) {
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return nil
}

// boolean is a BOOLEAN attribute value.
func (r *resource) boolean(path string) any {
	if v, ok := r.attr(path).(bool); ok {
		return v
	}
	return nil
}

// ts is a TIMESTAMPTZ attribute value from an RFC 3339 string.
func (r *resource) ts(path string) any {
	s, ok := r.attr(path).(string)
	if !ok {
		return nil
	}
	return parseTS(s)
}

func parseTS(s string) any {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return t.UTC().Truncate(time.Microsecond)
}

// strList is a VARCHAR[] attribute value.
func (r *resource) strList(path string) any {
	items, ok := r.attr(path).([]any)
	if !ok {
		if r.attr(path) == nil {
			return nil
		}
		return []any{}
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		switch v := it.(type) {
		case string:
			out = append(out, v)
		case nil:
			out = append(out, nil)
		default:
			b, _ := json.Marshal(v)
			out = append(out, string(b))
		}
	}
	return out
}

// jsonAttr is a JSON attribute value (nil when absent or null).
func (r *resource) jsonAttr(path string) any {
	v := r.attr(path)
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return jsonText(b)
}
