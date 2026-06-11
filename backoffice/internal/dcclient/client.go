package dcclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/backoffice/internal/model"
)

// DCConn holds the connection details for a data center API.
type DCConn struct {
	APIURL string
	APIKey string
}

// NormalizeBaseURL validates and canonicalizes a data center API base URL.
//
// It accepts both in-cluster service DNS (e.g.
// "http://silkstrand-api.dc-us.svc.cluster.local:8080") and public https URLs.
// The returned value carries no trailing slash, so callers can safely
// concatenate "/internal/v1/..." without producing a double slash that would
// 404 against the DC router.
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("api_url is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("api_url is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("api_url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("api_url must include a host")
	}
	// Trim trailing slashes from the path so path joins don't double up;
	// drop query/fragment which are never meaningful on a base URL.
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// Client is an HTTP client for communicating with data center APIs.
type Client struct {
	http *http.Client
}

// New creates a new DC client with a 15-second timeout.
func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) do(method, url string, body any, conn DCConn) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("X-API-Key", conn.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.http.Do(req)
}

// HealthCheck performs a health check against the data center API.
// Uses /readyz (not /healthz) so the check exercises the DC's real
// dependencies (DB + Redis), not just process liveness.
//
// Parses the JSON response and requires status=="ok" so a service that is
// reachable but serving an unrelated placeholder body (e.g. an ingress
// default backend, or a stale Cloud Run revision pre-migration) is not
// reported as healthy.
func (c *Client) HealthCheck(conn DCConn) error {
	resp, err := c.http.Get(conn.APIURL + "/readyz")
	if err != nil {
		return fmt.Errorf("health check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return fmt.Errorf("health check response not JSON (likely Cloud Run placeholder): %w", err)
	}
	if body.Status != "ok" {
		return fmt.Errorf("health check status=%q (want \"ok\")", body.Status)
	}
	return nil
}

// CreateTenant creates a tenant in the data center.
func (c *Client) CreateTenant(conn DCConn, name string) (*model.DCTenantResponse, error) {
	resp, err := c.do("POST", conn.APIURL+"/internal/v1/tenants", map[string]string{"name": name}, conn)
	if err != nil {
		return nil, fmt.Errorf("creating tenant in DC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DC returned status %d: %s", resp.StatusCode, string(body))
	}

	var tenant model.DCTenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&tenant); err != nil {
		return nil, fmt.Errorf("decoding DC tenant response: %w", err)
	}
	return &tenant, nil
}

// UpdateTenant updates a tenant's status in the data center.
func (c *Client) UpdateTenant(conn DCConn, tenantID string, status string) error {
	resp, err := c.do("PUT", conn.APIURL+"/internal/v1/tenants/"+tenantID, map[string]string{"status": status}, conn)
	if err != nil {
		return fmt.Errorf("updating tenant in DC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DC returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// DeleteTenant removes (soft-deletes) a tenant in the data center.
func (c *Client) DeleteTenant(conn DCConn, tenantID string) error {
	resp, err := c.do("DELETE", conn.APIURL+"/internal/v1/tenants/"+tenantID, nil, conn)
	if err != nil {
		return fmt.Errorf("deleting tenant in DC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DC returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ListTenants lists all tenants in the data center.
func (c *Client) ListTenants(conn DCConn) ([]model.DCTenantResponse, error) {
	resp, err := c.do("GET", conn.APIURL+"/internal/v1/tenants", nil, conn)
	if err != nil {
		return nil, fmt.Errorf("listing tenants in DC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DC returned status %d: %s", resp.StatusCode, string(body))
	}

	var tenants []model.DCTenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&tenants); err != nil {
		return nil, fmt.Errorf("decoding DC tenants response: %w", err)
	}
	return tenants, nil
}

// ListAgents lists all agents in the data center.
func (c *Client) ListAgents(conn DCConn) ([]model.DCAgentResponse, error) {
	resp, err := c.do("GET", conn.APIURL+"/internal/v1/agents", nil, conn)
	if err != nil {
		return nil, fmt.Errorf("listing agents in DC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DC returned status %d: %s", resp.StatusCode, string(body))
	}

	var agents []model.DCAgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, fmt.Errorf("decoding DC agents response: %w", err)
	}
	return agents, nil
}

// GetStats retrieves stats from the data center.
func (c *Client) GetStats(conn DCConn) (*model.DCStatsResponse, error) {
	resp, err := c.do("GET", conn.APIURL+"/internal/v1/stats", nil, conn)
	if err != nil {
		return nil, fmt.Errorf("getting stats from DC: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DC returned status %d: %s", resp.StatusCode, string(body))
	}

	var stats model.DCStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decoding DC stats response: %w", err)
	}
	return &stats, nil
}
