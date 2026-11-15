// HTTP client for the HCP Terraform / Terraform Enterprise API v2 (JSON:API).
//
// Credentials come from a DuckDB secret of type `terraform`:
//
//	CREATE SECRET (TYPE terraform, TOKEN '...', ORGANIZATION 'acme');
//	CREATE SECRET (TYPE terraform, TOKEN '...', HOSTNAME 'tfe.example.com', ORGANIZATION 'acme');
//
// The secret is read on every call and never stored in scan state (which
// travels to DuckDB in HTTP continuation tokens). Every request is a GET: the
// worker is read-only.

package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Query-farm/vgi-go/vgi"
	"github.com/apache/arrow-go/v18/arrow"
)

// SecretType is the DuckDB secret type holding the API token.
const SecretType = "terraform"

// DefaultHostname is HCP Terraform's API host.
const DefaultHostname = "app.terraform.io"

// userAgent identifies the worker to the API.
const userAgent = "vgi-terraform-worker/" + Version

// requestTimeout bounds every HTTP attempt.
const requestTimeout = 60 * time.Second

// maxAttempts bounds retries of 429 / 5xx responses.
const maxAttempts = 6

// maxBackoff caps one wait between retries.
const maxBackoff = 60 * time.Second

func secretField(name, doc string, redact bool) arrow.Field {
	keys, vals := []string{"vgi_doc"}, []string{doc}
	if redact {
		keys, vals = append(keys, "redact"), append(vals, "true")
	}
	return arrow.Field{Name: name, Type: arrow.BinaryTypes.String, Nullable: true, Metadata: arrow.NewMetadata(keys, vals)}
}

// TerraformSecretType registers CREATE SECRET (TYPE terraform, ...).
var TerraformSecretType = vgi.SecretTypeSpec{
	Name: SecretType,
	Description: "HCP Terraform or Terraform Enterprise API token (user, team or organization token), the API " +
		"hostname and the default organization.",
	Schema: arrow.NewSchema([]arrow.Field{
		secretField("token", "API token: a user, team or organization token from HCP Terraform / Terraform Enterprise, "+
			"sent as 'Authorization: Bearer <token>'. Required. Read-only use: the worker only sends GET requests.", true),
		secretField("hostname", "API hostname, default app.terraform.io (HCP Terraform). Set it to your Terraform "+
			"Enterprise host, e.g. tfe.example.com.", false),
		secretField("organization", "Organization used when a function's organization := argument is omitted, "+
			"and by the unified tables.", false),
		secretField("base_url", "Full base URL overriding HOSTNAME, e.g. http://127.0.0.1:8080 for a local mirror or "+
			"a TFE instance on a non-standard port. The API path /api/v2 is appended.", false),
	}, &secretSchemaMetadata),
}

var secretSchemaMetadata = arrow.NewMetadata(
	[]string{"vgi_doc_url"},
	[]string{SourceURL + "#secrets-and-configuration"})

// config is a resolved terraform secret. Never stored in scan state.
type config struct {
	Token        string
	BaseURL      string // scheme://host, no trailing slash
	Organization string
}

// configFrom reads the terraform secret.
func configFrom(secrets vgi.Secrets) (*config, error) {
	s, ok := secrets.Lookup(SecretType)
	if !ok {
		return nil, errors.New("no terraform secret: create one with " +
			"CREATE SECRET (TYPE terraform, TOKEN '<api token>', ORGANIZATION '<org>') " +
			"(add HOSTNAME '<host>' for Terraform Enterprise)")
	}
	get := func(k string) string { return strings.TrimSpace(vgi.RenderSecretValue(s[k])) }
	c := &config{Token: get("token"), Organization: get("organization")}
	if c.Token == "" {
		return nil, errors.New("terraform secret: TOKEN is required")
	}
	switch base, host := get("base_url"), get("hostname"); {
	case base != "":
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("terraform secret: BASE_URL must be an http(s) URL, got %q", base)
		}
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(base, "/"), "/api/v2"), "/")
	case host != "":
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		c.BaseURL = "https://" + strings.TrimRight(host, "/")
	default:
		c.BaseURL = "https://" + DefaultHostname
	}
	return c, nil
}

// org resolves an organization argument against the secret's default.
func (c *config) org(arg *string) (string, error) {
	if arg != nil && strings.TrimSpace(*arg) != "" {
		return strings.TrimSpace(*arg), nil
	}
	if c.Organization != "" {
		return c.Organization, nil
	}
	return "", errors.New("no organization: pass organization := '<name>' or set ORGANIZATION in the terraform secret")
}

// workspaceURL is the UI link of a workspace.
func (c *config) workspaceURL(org, workspace string) any {
	if org == "" || workspace == "" {
		return nil
	}
	return c.BaseURL + "/app/" + url.PathEscape(org) + "/workspaces/" + url.PathEscape(workspace)
}

// client issues API requests with one resolved config.
type client struct {
	cfg  *config
	http *http.Client
}

var sharedHTTP = &http.Client{Timeout: requestTimeout}

// connect resolves the session's secret into a client.
func connect(secrets vgi.Secrets) (*client, error) {
	cfg, err := configFrom(secrets)
	if err != nil {
		return nil, err
	}
	return &client{cfg: cfg, http: sharedHTTP}, nil
}

// apiError is a non-2xx API response.
type apiError struct {
	Status int
	Path   string
	Detail string
}

func (e *apiError) Error() string {
	hint := ""
	switch e.Status {
	case http.StatusUnauthorized:
		hint = " (check the TOKEN in the terraform secret)"
	case http.StatusNotFound:
		hint = " (the object does not exist, or the token cannot read it: HCP Terraform answers 404 for both)"
	}
	msg := fmt.Sprintf("HCP Terraform API %s: HTTP %d%s", e.Path, e.Status, hint)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// retryDelay picks the wait before retrying a 429 / 5xx response: the
// Retry-After or X-RateLimit-Reset header when present, else exponential
// backoff from 1 s.
func retryDelay(h http.Header, attempt int) time.Duration {
	for _, k := range []string{"Retry-After", "X-RateLimit-Reset"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				return min(time.Duration(f*float64(time.Second)), maxBackoff)
			}
			if t, err := http.ParseTime(v); err == nil {
				return min(max(time.Until(t), 0), maxBackoff)
			}
		}
	}
	return min(time.Duration(math.Pow(2, float64(attempt)))*time.Second, maxBackoff)
}

// get fetches one JSON:API document. path starts with "/" and is relative to
// /api/v2.
func (c *client) get(ctx context.Context, path string, q url.Values) (*document, error) {
	u := c.cfg.BaseURL + "/api/v2" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			if attempt+1 < maxAttempts && ctx.Err() == nil {
				slog.Debug("terraform api retry", "path", path, "error", err)
				if werr := wait(ctx, retryDelay(nil, attempt)); werr != nil {
					return nil, werr
				}
				continue
			}
			return nil, fmt.Errorf("HCP Terraform API %s: %w", path, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("HCP Terraform API %s: reading response: %w", path, err)
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout
		if retryable && attempt+1 < maxAttempts {
			d := retryDelay(resp.Header, attempt)
			slog.Debug("terraform api retry", "path", path, "status", resp.StatusCode, "wait", d)
			if werr := wait(ctx, d); werr != nil {
				return nil, werr
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, &apiError{Status: resp.StatusCode, Path: path, Detail: errorDetail(body)}
		}
		doc := &document{}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		if err := dec.Decode(doc); err != nil {
			return nil, fmt.Errorf("HCP Terraform API %s: decoding JSON:API document: %w", path, err)
		}
		return doc, nil
	}
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// errorDetail extracts the JSON:API error text from a response body.
func errorDetail(body []byte) string {
	var e struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Errors) == 0 {
		s := strings.TrimSpace(string(body))
		if len(s) > 300 {
			s = s[:300]
		}
		return s
	}
	parts := make([]string, 0, len(e.Errors))
	for _, raw := range e.Errors {
		var obj struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &obj) == nil && (obj.Title != "" || obj.Detail != "") {
			parts = append(parts, strings.TrimSpace(obj.Title+": "+obj.Detail))
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}
