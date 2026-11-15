package terraform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Query-farm/vgi-go/vgi"
)

func TestNewWorkerBuilds(t *testing.T) {
	if NewWorker() == nil {
		t.Fatal("NewWorker returned nil")
	}
	for _, d := range append(append([]*hcpDef{organizationsDef}, mainTables...), unifiedRunsDef, unifiedTaskRunsDef) {
		if d.Start == nil {
			t.Errorf("%s has no Start", d.Name)
		}
		if d.Schema().NumFields() == 0 {
			t.Errorf("%s has no columns", d.Name)
		}
	}
}

func secrets(fields map[string]any) vgi.Secrets {
	return vgi.Secrets{"tf": fields}
}

func TestConfigFrom(t *testing.T) {
	if _, err := configFrom(vgi.Secrets{}); err == nil || !strings.Contains(err.Error(), "CREATE SECRET") {
		t.Fatalf("missing secret: %v", err)
	}
	cases := []struct {
		fields map[string]any
		base   string
	}{
		{map[string]any{"type": "terraform", "token": "t"}, "https://app.terraform.io"},
		{map[string]any{"type": "terraform", "token": "t", "hostname": "tfe.example.com/"}, "https://tfe.example.com"},
		{map[string]any{"type": "terraform", "token": "t", "base_url": "http://127.0.0.1:9/api/v2/"}, "http://127.0.0.1:9"},
	}
	for _, c := range cases {
		cfg, err := configFrom(secrets(c.fields))
		if err != nil {
			t.Fatalf("%v: %v", c.fields, err)
		}
		if cfg.BaseURL != c.base {
			t.Errorf("%v: base %q, want %q", c.fields, cfg.BaseURL, c.base)
		}
	}
	if _, err := configFrom(secrets(map[string]any{"type": "terraform", "hostname": "x"})); err == nil {
		t.Error("a secret without TOKEN must be rejected")
	}
	if _, err := configFrom(secrets(map[string]any{"type": "terraform", "token": "t", "base_url": "ftp://x"})); err == nil {
		t.Error("a non-http BASE_URL must be rejected")
	}
	cfg := &config{Organization: "acme"}
	if o, _ := cfg.org(nil); o != "acme" {
		t.Errorf("default org %q", o)
	}
	other := "globex"
	if o, _ := cfg.org(&other); o != "globex" {
		t.Errorf("override org %q", o)
	}
	if _, err := (&config{}).org(nil); err == nil {
		t.Error("no organization must be an error")
	}
}

func TestRetryDelay(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "2")
	if d := retryDelay(h, 0); d != 2*time.Second {
		t.Errorf("Retry-After: %v", d)
	}
	h = http.Header{}
	h.Set("X-RateLimit-Reset", "0.25")
	if d := retryDelay(h, 3); d != 250*time.Millisecond {
		t.Errorf("X-RateLimit-Reset: %v", d)
	}
	if d := retryDelay(nil, 2); d != 4*time.Second {
		t.Errorf("backoff: %v", d)
	}
	if d := retryDelay(nil, 20); d != maxBackoff {
		t.Errorf("backoff cap: %v", d)
	}
}

func TestClientRetriesAndAuth(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"status":"401","title":"unauthorized"}]}`))
			return
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Path != "/api/v2/organizations" || r.URL.Query().Get("page[size]") != "100" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"acme","type":"organizations","attributes":{"name":"acme","n":12345678901234}}],
			"meta":{"pagination":{"current-page":1,"next-page":2}}}`))
	}))
	defer srv.Close()
	c := &client{cfg: &config{Token: "tok", BaseURL: srv.URL}, http: srv.Client()}
	doc, err := c.get(context.Background(), "/organizations", url.Values{"page[size]": {"100"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("want one retry, got %d calls", calls.Load())
	}
	rs, _ := doc.list()
	if len(rs) != 1 || rs[0].i64("n") != int64(12345678901234) || doc.nextPage() != 2 {
		t.Errorf("decoded %+v next %d", rs, doc.nextPage())
	}
	c.cfg.Token = "bad"
	_, err = c.get(context.Background(), "/organizations", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "TOKEN") {
		t.Errorf("401: %v", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Error("the token must not appear in errors")
	}
}

func TestResourceAccessors(t *testing.T) {
	var r resource
	if err := decodeNumber([]byte(`{"id":"x","attributes":{"s":"a","n":"12.5","i":7,"b":true,"t":"2026-11-13T09:20:00.000Z",
		"l":["a",1],"o":{"k":{"v":3}}},"relationships":{"p":{"data":{"id":"prj-1","type":"projects"}},"q":{"data":null},
		"m":{"data":[{"id":"a","type":"t"},{"id":"b","type":"t"}]}}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.str("s") != "a" || r.f64("n") != 12.5 || r.i64("i") != int64(7) || r.boolean("b") != true {
		t.Error("scalar accessors")
	}
	if r.i64("o.k.v") != int64(3) || r.attr("o.missing.x") != nil {
		t.Error("dotted paths")
	}
	if ts, ok := r.ts("t").(time.Time); !ok || ts.Unix() != 1794561600 {
		t.Errorf("timestamp %v", r.ts("t"))
	}
	if !reflect.DeepEqual(r.strList("l"), []any{"a", "1"}) {
		t.Errorf("list %v", r.strList("l"))
	}
	if r.relID("p") != "prj-1" || r.relID("q") != "" || r.relID("zz") != "" {
		t.Error("relationships")
	}
	if !reflect.DeepEqual(r.relIDs("m"), []string{"a", "b"}) {
		t.Error("to-many relationship")
	}
	var nilRes *resource
	if nilRes.str("x") != nil || nilRes.relID("x") != "" {
		t.Error("nil resource accessors must be NULL")
	}
}

func TestRunStatusMapping(t *testing.T) {
	for raw, want := range map[string]string{
		"applied": "success", "planned_and_finished": "success", "errored": "failed", "discarded": "cancelled",
		"plan_queued": "queued", "planning": "running", "policy_soft_failed": "running", "cost_estimating": "running",
	} {
		if got := unifiedRunStatus(raw); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	got, ok := rawStatusesFor([]string{"cancelled"})
	if !ok || !reflect.DeepEqual(got, []string{"canceled", "discarded", "force_canceled"}) {
		t.Errorf("cancelled -> %v", got)
	}
	if _, ok := rawStatusesFor([]string{"success", "running"}); ok {
		t.Error("running must not push down")
	}
	if got, _ := rawStatusesFor([]string{"bogus"}); !reflect.DeepEqual(got, []string{"bogus"}) {
		t.Errorf("unknown value %v", got)
	}
	for raw, want := range map[string]string{"finished": "success", "unreachable": "skipped", "managed_queued": "queued", "errored": "failed"} {
		if got := planApplyStatus(raw); got != want {
			t.Errorf("phase %s: %s, want %s", raw, got, want)
		}
	}
	if runTrigger("tfe-ui") != "manual" || runTrigger("tfe-configuration-version") != "event" || runTrigger("") != nil {
		t.Error("trigger mapping")
	}
}

func TestMasking(t *testing.T) {
	var v any
	_ = decodeJSON([]byte(`{"a":"x","b":{"c":"secret","d":[1,"s2"]},"e":null}`), &v)
	var mirror any
	_ = decodeJSON([]byte(`{"b":{"c":true,"d":[false,true]},"e":true}`), &mirror)
	got, _ := json.Marshal(maskMirror(v, mirror))
	if string(got) != `{"a":"x","b":{"c":"(sensitive value)","d":[1,"(sensitive value)"]},"e":null}` {
		t.Errorf("maskMirror: %s", got)
	}
	var steps []stateStep
	_ = decodeJSON([]byte(`[{"type":"get_attr","value":"b"},{"type":"get_attr","value":"d"},{"type":"index","value":{"value":1,"type":"number"}}]`), &steps)
	path := []any{}
	for _, s := range steps {
		path = append(path, stepKey(s))
	}
	if pathString(path) != "b.d[1]" {
		t.Errorf("pathString %q", pathString(path))
	}
	_ = decodeJSON([]byte(`{"a":"x","b":{"c":"secret","d":[1,"s2"]}}`), &v)
	got, _ = json.Marshal(maskPath(v, path))
	if string(got) != `{"a":"x","b":{"c":"secret","d":[1,"(sensitive value)"]}}` {
		t.Errorf("maskPath: %s", got)
	}
}

func TestStateAndPlanSamples(t *testing.T) {
	b, err := readDocument("sample:basic.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := stateRows(b)
	if err != nil || len(rows) != 8 {
		t.Fatalf("state rows %d %v", len(rows), err)
	}
	for _, r := range rows {
		if strings.Contains(string(r[8].(jsonText)), "y4NMdpfOCW9Quij8lAgb") {
			t.Errorf("%s leaks the sensitive password", r[0])
		}
	}
	out, _ := stateOutputRows(b)
	if out[0][0] != "db_password" || out[0][2] != nil {
		t.Errorf("sensitive output not masked: %v", out[0])
	}
	p, _ := readDocument("sample:change.plan.json")
	prow, err := planRows(p)
	if err != nil || len(prow) != 9 {
		t.Fatalf("plan rows %d %v", len(prow), err)
	}
	if _, err := planRows(b); err == nil {
		t.Error("a state is not a plan")
	}
	if _, err := stateRows([]byte(`{"version": 3}`)); err == nil || !strings.Contains(err.Error(), "version 3") {
		t.Errorf("v3 state: %v", err)
	}
	if _, err := stateRows([]byte(`{"version": 4,`)); err == nil {
		t.Error("truncated state must fail")
	}
	if _, err := readDocument("sample:missing"); err == nil || !strings.Contains(err.Error(), "sample:basic.tfstate") {
		t.Errorf("missing sample: %v", err)
	}
}

func TestChangedAttributes(t *testing.T) {
	var c planChange
	_ = decodeJSON([]byte(`{"actions":["update"],"before":{"a":1,"b":"x","c":"same"},"after":{"a":2,"c":"same","d":true},
		"after_unknown":{"e":true,"f":{"g":false}}}`), &c)
	if got := changedAttributes(c); !reflect.DeepEqual(got, []string{"a", "b", "d", "e"}) {
		t.Errorf("changed %v", got)
	}
	if actionSummary([]string{"create", "delete"}) != "replace" || actionSummary([]string{"no-op"}) != "no-op" {
		t.Error("actionSummary")
	}
	if instanceAddress("module.m", "data", "aws_ami", "x", "k") != `module.m.data.aws_ami.x["k"]` {
		t.Error("instanceAddress")
	}
}

func TestDestinationHost(t *testing.T) {
	r := &resource{Attributes: map[string]any{"url": "https://hooks.slack.com/services/T/B/SECRET"}}
	if destinationHost(r, nil) != "hooks.slack.com" {
		t.Errorf("host %v", destinationHost(r, nil))
	}
	r = &resource{Attributes: map[string]any{"value": "v", "sensitive": true}}
	if maskedValue(r, nil) != nil {
		t.Error("sensitive variable value must be NULL")
	}
}
