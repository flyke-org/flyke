package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	_ "modernc.org/sqlite"

	workerv1 "github.com/flyke-org/flyke/api/gen/flyke/worker/v1"
	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/server"
)

func ptr[T any](v T) *T { return &v }

const nsSecrets = "/api/v1/namespaces/default/secrets"

func TestSecretsRESTAPI(t *testing.T) {
	h := start(t)

	// A plain variable: its value is visible.
	var user v1.Secret
	if code := h.rest(http.MethodPost, nsSecrets, v1.SecretSpec{Key: "infoblox_username", Value: ptr("admin")}, &user); code != http.StatusCreated {
		t.Fatalf("create variable: %d", code)
	}
	if user.ID == "" || user.Type != "Private" || user.Sensitive || user.Value != "admin" || user.Namespace != "default" {
		t.Fatalf("variable: %+v", user)
	}

	// A sensitive secret: its value is never returned.
	var pw v1.Secret
	if code := h.rest(http.MethodPost, nsSecrets, v1.SecretSpec{Key: "infoblox_password", Value: ptr("s3cret"), Sensitive: ptr(true)}, &pw); code != http.StatusCreated {
		t.Fatalf("create sensitive: %d", code)
	}
	if !pw.Sensitive || pw.Value != "" {
		t.Fatalf("sensitive create leaked: %+v", pw)
	}
	var got v1.Secret
	if code := h.rest(http.MethodGet, nsSecrets+"/infoblox_password", nil, &got); code != http.StatusOK || got.Value != "" || !got.Sensitive {
		t.Fatalf("get sensitive: %d %+v", code, got)
	}

	// Errors.
	for name, tc := range map[string]struct {
		method, path string
		body         v1.SecretSpec
		want         int
	}{
		"duplicate":        {http.MethodPost, nsSecrets, v1.SecretSpec{Key: "infoblox_password", Value: ptr("x")}, http.StatusConflict},
		"bad key":          {http.MethodPost, nsSecrets, v1.SecretSpec{Key: "bad key!", Value: ptr("x")}, http.StatusUnprocessableEntity},
		"bad type":         {http.MethodPost, nsSecrets, v1.SecretSpec{Key: "k", Value: ptr("x"), Type: "Weird"}, http.StatusUnprocessableEntity},
		"private on /":     {http.MethodPost, "/api/v1/secrets", v1.SecretSpec{Key: "k", Value: ptr("x"), Type: "Private"}, http.StatusUnprocessableEntity},
		"key mismatch":     {http.MethodPut, nsSecrets + "/a", v1.SecretSpec{Key: "b", Value: ptr("x")}, http.StatusUnprocessableEntity},
		"create w/o value": {http.MethodPut, nsSecrets + "/new_key", v1.SecretSpec{}, http.StatusUnprocessableEntity},
		"flip w/o value":   {http.MethodPut, nsSecrets + "/infoblox_password", v1.SecretSpec{Sensitive: ptr(false)}, http.StatusUnprocessableEntity},
	} {
		if code := h.rest(tc.method, tc.path, tc.body, nil); code != tc.want {
			t.Errorf("%s: got %d, want %d", name, code, tc.want)
		}
	}

	// PUT updates in place; an omitted value keeps the stored one.
	var updated v1.Secret
	if code := h.rest(http.MethodPut, nsSecrets+"/infoblox_username", v1.SecretSpec{Value: ptr("root")}, &updated); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	if updated.ID != user.ID || !updated.CreatedAt.Equal(user.CreatedAt) || updated.Value != "root" {
		t.Fatalf("put response: %+v", updated)
	}
	if code := h.rest(http.MethodPut, nsSecrets+"/infoblox_password", v1.SecretSpec{}, &got); code != http.StatusOK || !got.Sensitive {
		t.Fatalf("put keep: %d %+v", code, got)
	}

	// Shared secrets are visible from every namespace; Private ones override them.
	if code := h.rest(http.MethodPost, "/api/v1/secrets", v1.SecretSpec{Key: "smtp_host", Value: ptr("smtp.shared")}, nil); code != http.StatusCreated {
		t.Fatalf("create shared: %d", code)
	}
	var tok v1.Secret
	if code := h.rest(http.MethodPost, "/api/v1/namespaces/team-a/secrets", v1.SecretSpec{Key: "api_token", Value: ptr("t"), Sensitive: ptr(true), Type: "Shared"}, &tok); code != http.StatusCreated || tok.Type != "Shared" || tok.Namespace != "" {
		t.Fatalf("create shared from a namespace route: %d %+v", code, tok)
	}
	for _, ns := range []string{"default", "team-a", "team-b"} {
		if code := h.rest(http.MethodGet, "/api/v1/namespaces/"+ns+"/secrets/smtp_host", nil, &got); code != http.StatusOK || got.Value != "smtp.shared" || got.Type != "Shared" {
			t.Fatalf("shared from %s: %d %+v", ns, code, got)
		}
	}
	h.rest(http.MethodPut, "/api/v1/namespaces/team-b/secrets/smtp_host", v1.SecretSpec{Value: ptr("smtp.b")}, nil)
	if h.rest(http.MethodGet, "/api/v1/namespaces/team-b/secrets/smtp_host", nil, &got); got.Value != "smtp.b" || got.Type != "Private" {
		t.Fatalf("override: %+v", got)
	}
	if h.rest(http.MethodGet, "/api/v1/namespaces/team-b/secrets/smtp_host?type=Shared", nil, &got); got.Value != "smtp.shared" {
		t.Fatalf("explicit shared lookup: %+v", got)
	}

	// Listing: Private secrets of the namespace plus Shared ones, sensitive values hidden.
	var l v1.List[v1.Secret]
	h.rest(http.MethodGet, nsSecrets, nil, &l)
	var keys []string
	for _, s := range l.Items {
		keys = append(keys, s.Key+"/"+s.Type)
		if s.Sensitive && s.Value != "" {
			t.Fatalf("list leaked sensitive %s", s.Key)
		}
	}
	if strings.Join(keys, ",") != "api_token/Shared,infoblox_password/Private,infoblox_username/Private,smtp_host/Shared" {
		t.Fatalf("list: %v", keys)
	}
	h.rest(http.MethodGet, "/api/v1/secrets", nil, &l)
	if len(l.Items) != 2 {
		t.Fatalf("shared list: %+v", l.Items)
	}

	// Delete.
	if code := h.rest(http.MethodDelete, nsSecrets+"/smtp_host", nil, nil); code != http.StatusNotFound {
		t.Fatalf("namespace delete must not remove a shared secret: %d", code)
	}
	if code := h.rest(http.MethodDelete, "/api/v1/secrets/smtp_host", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete shared: %d", code)
	}
	if code := h.rest(http.MethodDelete, nsSecrets+"/infoblox_password", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code := h.rest(http.MethodGet, nsSecrets+"/infoblox_password", nil, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
}

func TestTaskReadsSecretsOverTheWorkerProtocol(t *testing.T) {
	h := start(t)
	ctx := context.Background()
	h.rest(http.MethodPost, nsSecrets, v1.SecretSpec{Key: "db_password", Value: ptr("pa55"), Sensitive: ptr(true)}, nil)
	h.rest(http.MethodPost, "/api/v1/secrets", v1.SecretSpec{Key: "region", Value: ptr("eu-west-1")}, nil)
	h.rest(http.MethodPost, "/api/v1/namespaces/other/secrets", v1.SecretSpec{Key: "foreign", Value: ptr("no")}, nil)

	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"s"}`)}, nil)
	step := h.poll()

	// The workflow body (a step) cannot read secrets: it must stay deterministic.
	_, err := h.worker.GetSecret(ctx, auth(h, &workerv1.GetSecretRequest{LeaseId: step.LeaseId, Key: "region"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("step read: %v", err)
	}
	h.worker.CompleteStep(ctx, auth(h, &workerv1.CompleteStepRequest{LeaseId: step.LeaseId, Commands: []*workerv1.Command{
		{Ref: 1, Kind: &workerv1.Command_ScheduleTask{ScheduleTask: &workerv1.ScheduleTask{Name: "work", Args: payload([]any{1})}}},
	}}))
	a := h.poll()
	h.worker.AckTask(ctx, auth(h, &workerv1.AckTaskRequest{LeaseId: a.LeaseId}))

	resp, err := h.worker.GetSecret(ctx, auth(h, &workerv1.GetSecretRequest{LeaseId: a.LeaseId, Key: "db_password"}))
	if err != nil || resp.Msg.Value != "pa55" || !resp.Msg.Sensitive || resp.Msg.Type != "Private" {
		t.Fatalf("sensitive read: %v %+v", err, resp)
	}
	resp, err = h.worker.GetSecret(ctx, auth(h, &workerv1.GetSecretRequest{LeaseId: a.LeaseId, Key: "region"}))
	if err != nil || resp.Msg.Value != "eu-west-1" || resp.Msg.Type != "Shared" {
		t.Fatalf("shared read: %v %+v", err, resp)
	}
	if _, err := h.worker.GetSecret(ctx, auth(h, &workerv1.GetSecretRequest{LeaseId: a.LeaseId, Key: "foreign"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("other namespace's secret must be invisible: %v", err)
	}
	if _, err := h.worker.GetSecret(ctx, auth(h, &workerv1.GetSecretRequest{LeaseId: "bogus", Key: "region"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unknown lease: %v", err)
	}
	if _, err := h.worker.GetSecret(ctx, connect.NewRequest(&workerv1.GetSecretRequest{LeaseId: a.LeaseId, Key: "region"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no credential: %v", err)
	}
	// Once the task is done its lease no longer grants access.
	h.worker.CompleteTask(ctx, auth(h, &workerv1.CompleteTaskRequest{LeaseId: a.LeaseId, Result: payload(1)}))
	if _, err := h.worker.GetSecret(ctx, auth(h, &workerv1.GetSecretRequest{LeaseId: a.LeaseId, Key: "region"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("finished lease: %v", err)
	}
}

func TestSensitiveSecretsAreEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	open := func() *server.Server {
		srv, err := server.New(server.Config{Addr: "127.0.0.1:0", DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return srv
	}
	srv := open()
	h := &harness{t: t, srv: srv}
	h.rest(http.MethodPost, nsSecrets, v1.SecretSpec{Key: "pw", Value: ptr("plaintext-canary"), Sensitive: ptr(true)}, nil)
	h.rest(http.MethodPost, nsSecrets, v1.SecretSpec{Key: "user", Value: ptr("visible-user")}, nil)
	_ = srv.Shutdown(context.Background())

	if info, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", err, info)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "flyke.db"))
	if err != nil {
		t.Fatal(err)
	}
	var pw, user string
	db.QueryRow(`SELECT value FROM secrets WHERE key='pw'`).Scan(&pw)
	db.QueryRow(`SELECT value FROM secrets WHERE key='user'`).Scan(&user)
	db.Close()
	if !strings.HasPrefix(pw, "v1:") || strings.Contains(pw, "plaintext-canary") {
		t.Fatalf("sensitive value stored as %q", pw)
	}
	if user != "visible-user" {
		t.Fatalf("variable stored as %q", user)
	}

	// After a restart the same key file decrypts the value for a task.
	srv = open()
	defer srv.Shutdown(context.Background())
	h = &harness{t: t, srv: srv}
	var got v1.Secret
	if code := h.rest(http.MethodGet, nsSecrets+"/pw", nil, &got); code != http.StatusOK || got.Value != "" {
		t.Fatalf("after restart: %d %+v", code, got)
	}
}
