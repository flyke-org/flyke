// Package client is a small typed client for the Flyke REST API.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
)

type Client struct {
	BaseURL   string
	Token     string
	Namespace string
	// Cluster makes worker and pool calls use the cluster routes (admin):
	// every worker, and shared pools, instead of the namespace's own.
	Cluster bool
	HTTP    *http.Client
}

func New(baseURL, token, namespace string) *Client {
	if namespace == "" {
		namespace = "default"
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Namespace: namespace, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (%d): %s", e.Reason, e.Status, e.Message) }

func (c *Client) ns(path string) string {
	return "/api/v1/namespaces/" + url.PathEscape(c.Namespace) + path
}

// fleet is the base path of worker and pool routes.
func (c *Client) fleet(path string) string {
	if c.Cluster {
		return "/api/v1" + path
	}
	return c.ns(path)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rdr, contentType = b, "application/octet-stream"
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if rdr != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the control plane at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var st v1.Status
		data, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(data, &st) == nil && st.Message != "" {
			return &APIError{Status: resp.StatusCode, Reason: st.Reason, Message: st.Message}
		}
		return &APIError{Status: resp.StatusCode, Reason: http.StatusText(resp.StatusCode), Message: strings.TrimSpace(string(data))}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (c *Client) UploadBundle(ctx context.Context, r io.Reader) (v1.BundleUploadResponse, error) {
	var out v1.BundleUploadResponse
	return out, c.do(ctx, http.MethodPost, c.ns("/bundles"), r, &out)
}

func (c *Client) PutWorkflow(ctx context.Context, name string, spec v1.WorkflowSpec) (v1.Workflow, error) {
	var out v1.Workflow
	return out, c.do(ctx, http.MethodPut, c.ns("/workflows/"+url.PathEscape(name)), spec, &out)
}

func (c *Client) GetWorkflow(ctx context.Context, name string) (v1.Workflow, error) {
	var out v1.Workflow
	return out, c.do(ctx, http.MethodGet, c.ns("/workflows/"+url.PathEscape(name)), nil, &out)
}

func (c *Client) ListWorkflows(ctx context.Context) ([]v1.Workflow, error) {
	var out v1.List[v1.Workflow]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/workflows"), nil, &out)
}

func (c *Client) StartRun(ctx context.Context, workflow string, req v1.StartRunRequest) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodPost, c.ns("/workflows/"+url.PathEscape(workflow)+"/runs"), req, &out)
}

func (c *Client) GetRun(ctx context.Context, id string) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodGet, c.ns("/runs/"+url.PathEscape(id)), nil, &out)
}

func (c *Client) ListWorkflowVersions(ctx context.Context, name string) ([]v1.WorkflowVersion, error) {
	var out v1.List[v1.WorkflowVersion]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/workflows/"+url.PathEscape(name)+"/versions"), nil, &out)
}

func (c *Client) GetWorkflowVersion(ctx context.Context, name, version string) (v1.WorkflowVersion, error) {
	var out v1.WorkflowVersion
	return out, c.do(ctx, http.MethodGet, c.ns("/workflows/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version)), nil, &out)
}

// ActivateWorkflowVersion makes a registered version current (rollback).
func (c *Client) ActivateWorkflowVersion(ctx context.Context, name, version string) (v1.Workflow, error) {
	var out v1.Workflow
	return out, c.do(ctx, http.MethodPost, c.ns("/workflows/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version)+"/activate"), nil, &out)
}

func (c *Client) ListRuns(ctx context.Context, workflow, version, phase string, limit int) ([]v1.Run, error) {
	q := url.Values{}
	if workflow != "" {
		q.Set("workflow", workflow)
	}
	if version != "" {
		q.Set("version", version)
	}
	if phase != "" {
		q.Set("phase", phase)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out v1.List[v1.Run]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/runs?"+q.Encode()), nil, &out)
}

func (c *Client) RunTasks(ctx context.Context, id string) ([]v1.TaskRun, error) {
	var out v1.List[v1.TaskRun]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/runs/"+url.PathEscape(id)+"/tasks"), nil, &out)
}

func (c *Client) History(ctx context.Context, id string) ([]v1.HistoryEvent, error) {
	var out v1.List[v1.HistoryEvent]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/runs/"+url.PathEscape(id)+"/history"), nil, &out)
}

func (c *Client) CancelRun(ctx context.Context, id string) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodPost, c.ns("/runs/"+url.PathEscape(id)+"/cancel"), nil, &out)
}

func (c *Client) ResumeRun(ctx context.Context, id string) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodPost, c.ns("/runs/"+url.PathEscape(id)+"/resume"), nil, &out)
}

func (c *Client) RetryRun(ctx context.Context, id string, req v1.RetryRunRequest) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodPost, c.ns("/runs/"+url.PathEscape(id)+"/retry"), req, &out)
}

func (c *Client) ResolveTask(ctx context.Context, id string, ref int, req v1.ResolveTaskRequest) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodPost, c.ns(fmt.Sprintf("/runs/%s/tasks/%d/resolve", url.PathEscape(id), ref)), req, &out)
}

func (c *Client) Emit(ctx context.Context, name string, payload json.RawMessage) (v1.Event, error) {
	var out v1.Event
	return out, c.do(ctx, http.MethodPost, c.ns("/events"), v1.EmitEventRequest{Name: name, Payload: payload}, &out)
}

func (c *Client) ListWorkers(ctx context.Context, selector string) ([]v1.Worker, error) {
	var out v1.List[v1.Worker]
	return out.Items, c.do(ctx, http.MethodGet, c.fleet("/workers?labelSelector="+url.QueryEscape(selector)), nil, &out)
}

func (c *Client) DeleteWorker(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, c.fleet("/workers/"+url.PathEscape(name)), nil, nil)
}

func (c *Client) WorkerAction(ctx context.Context, name, action string) (v1.Worker, error) {
	var out v1.Worker
	return out, c.do(ctx, http.MethodPost, c.fleet("/workers/"+url.PathEscape(name)+"/"+action), nil, &out)
}

func (c *Client) ListPools(ctx context.Context) ([]v1.Pool, error) {
	var out v1.List[v1.Pool]
	return out.Items, c.do(ctx, http.MethodGet, c.fleet("/pools"), nil, &out)
}

func (c *Client) PutPool(ctx context.Context, name string, spec v1.PoolSpec) (v1.Pool, error) {
	var out v1.Pool
	return out, c.do(ctx, http.MethodPut, c.fleet("/pools/"+url.PathEscape(name)), spec, &out)
}

func (c *Client) DeletePool(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, c.fleet("/pools/"+url.PathEscape(name)), nil, nil)
}

func (c *Client) CreateJoinToken(ctx context.Context, pool string, req v1.CreateJoinTokenRequest) (v1.JoinToken, error) {
	var out v1.JoinToken
	return out, c.do(ctx, http.MethodPost, c.fleet("/pools/"+url.PathEscape(pool)+"/join-tokens"), req, &out)
}

func (c *Client) ListLogs(ctx context.Context, runID string) ([]v1.LogEntry, error) {
	var out v1.List[v1.LogEntry]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/runs/"+url.PathEscape(runID)+"/logs"), nil, &out)
}

// LogEvent is one item of a followed log stream.
type LogEvent struct {
	Log *v1.LogEntry
	// End is set on the final event with the run's terminal state.
	End *v1.Run
}

// FollowLogs streams logs (SSE) until the run ends; it reconnects on
// transient failures, resuming after the last received entry.
func (c *Client) FollowLogs(ctx context.Context, runID string, after int64, fn func(LogEvent) error) error {
	streamer := &http.Client{} // no timeout: long-lived stream
	failures := 0
	for {
		path := c.ns("/runs/" + url.PathEscape(runID) + "/logs?follow=true&after=" + strconv.FormatInt(after, 10))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "text/event-stream")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		resp, err := streamer.Do(req)
		if err == nil && resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("log stream: %s: %s", resp.Status, strings.TrimSpace(string(data)))
		}
		if err == nil {
			var ended bool
			ended, after, err = readSSE(resp.Body, after, fn)
			resp.Body.Close()
			if ended || err == errStop {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failures++; failures > 30 {
			return fmt.Errorf("log stream lost: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

var errStop = fmt.Errorf("stop")

func readSSE(r io.Reader, after int64, fn func(LogEvent) error) (bool, int64, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var event string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			payload := data.String()
			data.Reset()
			switch event {
			case "log":
				var e v1.LogEntry
				if json.Unmarshal([]byte(payload), &e) == nil {
					if e.ID > after {
						after = e.ID
					}
					if err := fn(LogEvent{Log: &e}); err != nil {
						return false, after, errStop
					}
				}
			case "end":
				var run v1.Run
				if json.Unmarshal([]byte(payload), &run) == nil {
					_ = fn(LogEvent{End: &run})
				}
				return true, after, nil
			}
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	return false, after, sc.Err()
}

// secretsPath addresses Shared secrets (/api/v1/secrets) or the namespace's
// view (/api/v1/namespaces/{ns}/secrets).
func (c *Client) secretsPath(shared bool, key string) string {
	p := c.ns("/secrets")
	if shared {
		p = "/api/v1/secrets"
	}
	if key != "" {
		p += "/" + url.PathEscape(key)
	}
	return p
}

// ListSecrets lists the Shared secrets, or the namespace's Private secrets
// plus the Shared ones. Sensitive values are never returned.
func (c *Client) ListSecrets(ctx context.Context, shared bool) ([]v1.Secret, error) {
	var out v1.List[v1.Secret]
	return out.Items, c.do(ctx, http.MethodGet, c.secretsPath(shared, ""), nil, &out)
}

// GetSecret returns a Shared secret, or the one a run in the namespace sees.
func (c *Client) GetSecret(ctx context.Context, shared bool, key string) (v1.Secret, error) {
	var out v1.Secret
	return out, c.do(ctx, http.MethodGet, c.secretsPath(shared, key), nil, &out)
}

func (c *Client) PutSecret(ctx context.Context, shared bool, key string, spec v1.SecretSpec) (v1.Secret, error) {
	if shared {
		spec.Type = "Shared"
	} else {
		spec.Type = "Private"
	}
	var out v1.Secret
	return out, c.do(ctx, http.MethodPut, c.secretsPath(shared, key), spec, &out)
}

func (c *Client) DeleteSecret(ctx context.Context, shared bool, key string) error {
	return c.do(ctx, http.MethodDelete, c.secretsPath(shared, key), nil, nil)
}

// ------------------------------------------------------------ namespaces ----

func (c *Client) WhoAmI(ctx context.Context) (v1.WhoAmI, error) {
	var out v1.WhoAmI
	return out, c.do(ctx, http.MethodGet, "/api/v1/whoami", nil, &out)
}

func (c *Client) ListNamespaces(ctx context.Context) ([]v1.Namespace, error) {
	var out v1.List[v1.Namespace]
	return out.Items, c.do(ctx, http.MethodGet, "/api/v1/namespaces", nil, &out)
}

func (c *Client) GetNamespace(ctx context.Context, name string) (v1.Namespace, error) {
	var out v1.Namespace
	return out, c.do(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(name), nil, &out)
}

func (c *Client) CreateNamespace(ctx context.Context, req v1.CreateNamespaceRequest) (v1.Namespace, error) {
	var out v1.Namespace
	return out, c.do(ctx, http.MethodPost, "/api/v1/namespaces", req, &out)
}

func (c *Client) UpdateNamespace(ctx context.Context, name string, spec v1.NamespaceSpec) (v1.Namespace, error) {
	var out v1.Namespace
	return out, c.do(ctx, http.MethodPut, "/api/v1/namespaces/"+url.PathEscape(name), spec, &out)
}

func (c *Client) DeleteNamespace(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/namespaces/"+url.PathEscape(name), nil, nil)
}

func (c *Client) ListAccessTokens(ctx context.Context) ([]v1.AccessToken, error) {
	var out v1.List[v1.AccessToken]
	return out.Items, c.do(ctx, http.MethodGet, c.ns("/tokens"), nil, &out)
}

func (c *Client) CreateAccessToken(ctx context.Context, req v1.CreateAccessTokenRequest) (v1.AccessToken, error) {
	var out v1.AccessToken
	return out, c.do(ctx, http.MethodPost, c.ns("/tokens"), req, &out)
}

func (c *Client) DeleteAccessToken(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, c.ns("/tokens/"+url.PathEscape(id)), nil, nil)
}

// ------------------------------------------------------------- schedules ----

func (c *Client) ListSchedules(ctx context.Context, workflow string) ([]v1.Schedule, error) {
	var out v1.List[v1.Schedule]
	path := c.ns("/schedules")
	if workflow != "" {
		path += "?workflow=" + url.QueryEscape(workflow)
	}
	return out.Items, c.do(ctx, http.MethodGet, path, nil, &out)
}

func (c *Client) GetSchedule(ctx context.Context, name string) (v1.Schedule, error) {
	var out v1.Schedule
	return out, c.do(ctx, http.MethodGet, c.ns("/schedules/"+url.PathEscape(name)), nil, &out)
}

func (c *Client) PutSchedule(ctx context.Context, name string, spec v1.ScheduleSpec) (v1.Schedule, error) {
	var out v1.Schedule
	return out, c.do(ctx, http.MethodPut, c.ns("/schedules/"+url.PathEscape(name)), spec, &out)
}

func (c *Client) DeleteSchedule(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, c.ns("/schedules/"+url.PathEscape(name)), nil, nil)
}

// ScheduleAction pauses or resumes a schedule (action "pause" | "resume").
func (c *Client) ScheduleAction(ctx context.Context, name, action string) (v1.Schedule, error) {
	var out v1.Schedule
	return out, c.do(ctx, http.MethodPost, c.ns("/schedules/"+url.PathEscape(name)+"/"+action), nil, &out)
}

// TriggerSchedule starts a run of the schedule now.
func (c *Client) TriggerSchedule(ctx context.Context, name string) (v1.Run, error) {
	var out v1.Run
	return out, c.do(ctx, http.MethodPost, c.ns("/schedules/"+url.PathEscape(name)+"/trigger"), nil, &out)
}

func (c *Client) PreviewSchedule(ctx context.Context, req v1.SchedulePreviewRequest) (v1.SchedulePreview, error) {
	var out v1.SchedulePreview
	return out, c.do(ctx, http.MethodPost, c.ns("/schedules/preview"), req, &out)
}
