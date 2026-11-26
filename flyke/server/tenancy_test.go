package server_test

// Multi-tenancy: a namespace is a tenant. These tests drive two tenants and
// the admin through the public protocols and check that nothing crosses.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	workerv1 "github.com/flyke-org/flyke/api/gen/flyke/worker/v1"
	"github.com/flyke-org/flyke/api/gen/flyke/worker/v1/workerv1connect"
	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/server"
)

const adminToken = "admin-secret"

type tenancy struct {
	t      *testing.T
	srv    *server.Server
	worker workerv1connect.WorkerServiceClient
	a, b   string // access tokens of namespaces team-a and team-b
}

func startTenancy(t *testing.T) *tenancy {
	t.Helper()
	srv, err := server.New(server.Config{
		Addr: "127.0.0.1:0", InMemory: true, AdminToken: adminToken, ResyncInterval: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	x := &tenancy{t: t, srv: srv, worker: workerv1connect.NewWorkerServiceClient(http.DefaultClient, srv.URL())}
	x.a, x.b = x.tenant("team-a"), x.tenant("team-b")
	return x
}

// tenant creates a namespace and returns an access token for it.
func (x *tenancy) tenant(ns string) string {
	x.t.Helper()
	x.must(http.StatusCreated, adminToken, http.MethodPost, "/api/v1/namespaces", v1.CreateNamespaceRequest{Name: ns}, nil)
	var tok v1.AccessToken
	x.must(http.StatusCreated, adminToken, http.MethodPost, "/api/v1/namespaces/"+ns+"/tokens", v1.CreateAccessTokenRequest{Name: "ci"}, &tok)
	if tok.Token == "" || tok.Namespace != ns {
		x.t.Fatalf("token: %+v", tok)
	}
	return tok.Token
}

func (x *tenancy) call(token, method, path string, body, out any) int {
	x.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, x.srv.URL()+path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (x *tenancy) must(want int, token, method, path string, body, out any) {
	x.t.Helper()
	if got := x.call(token, method, path, body, out); got != want {
		x.t.Fatalf("%s %s = %d, want %d", method, path, got, want)
	}
}

// register joins a worker with a join token created by `token` at `path`.
func (x *tenancy) register(token, path, name string) (string, error) {
	x.t.Helper()
	var jt v1.JoinToken
	x.must(http.StatusCreated, token, http.MethodPost, path, v1.CreateJoinTokenRequest{}, &jt)
	reg, err := x.worker.Register(context.Background(), connect.NewRequest(&workerv1.RegisterRequest{
		JoinToken: jt.Token, Name: name, Hostname: "host", Runtimes: []string{"python/3.12"},
		Capacity: &workerv1.Capacity{Slots: 4}, ProtocolVersion: "v1",
	}))
	if err != nil {
		return "", err
	}
	return reg.Msg.Credential, nil
}

func (x *tenancy) poll(cred string) []*workerv1.Assignment {
	x.t.Helper()
	req := connect.NewRequest(&workerv1.PollRequest{MaxItems: 4, WaitSeconds: 1})
	req.Header().Set("Authorization", "Bearer "+cred)
	resp, err := x.worker.Poll(context.Background(), req)
	if err != nil {
		x.t.Fatal(err)
	}
	return resp.Msg.Assignments
}

// deploy uploads a bundle and registers Pipeline in ns, returning the digest.
func (x *tenancy) deploy(token, ns string, bundle []byte) string {
	x.t.Helper()
	var up v1.BundleUploadResponse
	x.must(http.StatusCreated, token, http.MethodPost, "/api/v1/namespaces/"+ns+"/bundles", bundle, &up)
	x.must(http.StatusOK, token, http.MethodPut, "/api/v1/namespaces/"+ns+"/workflows/Pipeline",
		v1.WorkflowSpec{Manifest: json.RawMessage(manifest), Source: v1.Source{BundleDigest: up.Digest}}, nil)
	return up.Digest
}

func (x *tenancy) run(token, ns string) string {
	x.t.Helper()
	var r v1.Run
	x.must(http.StatusCreated, token, http.MethodPost, "/api/v1/namespaces/"+ns+"/workflows/Pipeline/runs",
		v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"k"}`)}, &r)
	return r.Metadata.Name
}

func TestTenantAPIIsolation(t *testing.T) {
	x := startTenancy(t)

	// Authentication.
	x.must(http.StatusUnauthorized, "", http.MethodGet, "/api/v1/whoami", nil, nil)
	x.must(http.StatusUnauthorized, "flyke_at_nope.nope", http.MethodGet, "/api/v1/whoami", nil, nil)
	var me v1.WhoAmI
	x.must(http.StatusOK, x.a, http.MethodGet, "/api/v1/whoami", nil, &me)
	if me.Admin || me.Namespace != "team-a" || me.Token != "ci" {
		t.Fatalf("whoami tenant: %+v", me)
	}
	x.must(http.StatusOK, adminToken, http.MethodGet, "/api/v1/whoami", nil, &me)
	if !me.Admin {
		t.Fatalf("whoami admin: %+v", me)
	}

	// A tenant lists only its own namespace; the admin lists all.
	var nss v1.List[v1.Namespace]
	x.must(http.StatusOK, x.a, http.MethodGet, "/api/v1/namespaces", nil, &nss)
	if len(nss.Items) != 1 || nss.Items[0].Metadata.Name != "team-a" {
		t.Fatalf("tenant sees namespaces %+v", nss.Items)
	}
	x.must(http.StatusOK, adminToken, http.MethodGet, "/api/v1/namespaces", nil, &nss)
	if len(nss.Items) != 3 { // default, team-a, team-b
		t.Fatalf("admin sees %d namespaces, want 3", len(nss.Items))
	}

	x.deploy(x.a, "team-a", []byte("bundle-a"))
	runA := x.run(x.a, "team-a")
	x.must(http.StatusCreated, x.a, http.MethodPost, "/api/v1/namespaces/team-a/secrets", v1.SecretSpec{Key: "k", Value: ptr("v")}, nil)

	// Every namespaced read and write of team-a is closed to team-b, and the
	// answer does not depend on whether the namespace or the object exists.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/namespaces/team-a"},
		{http.MethodGet, "/api/v1/namespaces/team-a/runs"},
		{http.MethodGet, "/api/v1/namespaces/team-a/runs/" + runA},
		{http.MethodGet, "/api/v1/namespaces/team-a/runs/" + runA + "/logs"},
		{http.MethodGet, "/api/v1/namespaces/team-a/runs/" + runA + "/history"},
		{http.MethodPost, "/api/v1/namespaces/team-a/runs/" + runA + "/cancel"},
		{http.MethodGet, "/api/v1/namespaces/team-a/workflows"},
		{http.MethodPost, "/api/v1/namespaces/team-a/workflows/Pipeline/runs"},
		{http.MethodGet, "/api/v1/namespaces/team-a/events"},
		{http.MethodPost, "/api/v1/namespaces/team-a/events"},
		{http.MethodGet, "/api/v1/namespaces/team-a/secrets"},
		{http.MethodGet, "/api/v1/namespaces/team-a/secrets/k"},
		{http.MethodGet, "/api/v1/namespaces/team-a/tokens"},
		{http.MethodPost, "/api/v1/namespaces/team-a/tokens"},
		{http.MethodGet, "/api/v1/namespaces/team-a/pools"},
		{http.MethodGet, "/api/v1/namespaces/team-a/workers"},
		{http.MethodGet, "/api/v1/namespaces/does-not-exist/runs"},
	} {
		x.must(http.StatusForbidden, x.b, c.method, c.path, nil, nil)
	}
	// A run id from another namespace is not found under one's own.
	x.must(http.StatusNotFound, x.b, http.MethodGet, "/api/v1/namespaces/team-b/runs/"+runA, nil, nil)

	// Cluster routes are for the admin.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/namespaces"},
		{http.MethodPut, "/api/v1/namespaces/team-a"},
		{http.MethodDelete, "/api/v1/namespaces/team-a"},
		{http.MethodGet, "/api/v1/workers"},
		{http.MethodGet, "/api/v1/pools"},
		{http.MethodPut, "/api/v1/pools/x"},
		{http.MethodPost, "/api/v1/pools/x/join-tokens"},
		{http.MethodGet, "/api/v1/secrets"},
		{http.MethodPost, "/api/v1/bundles"},
		{http.MethodGet, "/api/v1/playground/projects"},
	} {
		x.must(http.StatusForbidden, x.a, c.method, c.path, nil, nil)
	}

	// Shared secrets: written by the admin, readable by tenants, never
	// writable from a tenant's namespace route.
	x.must(http.StatusCreated, adminToken, http.MethodPost, "/api/v1/secrets", v1.SecretSpec{Key: "smtp", Value: ptr("shared")}, nil)
	var sec v1.Secret
	x.must(http.StatusOK, x.b, http.MethodGet, "/api/v1/namespaces/team-b/secrets/smtp", nil, &sec)
	if sec.Value != "shared" {
		t.Fatalf("shared secret: %+v", sec)
	}
	x.must(http.StatusForbidden, x.b, http.MethodPost, "/api/v1/namespaces/team-b/secrets", v1.SecretSpec{Key: "evil", Value: ptr("x"), Type: "Shared"}, nil)
	x.must(http.StatusForbidden, x.b, http.MethodPut, "/api/v1/namespaces/team-b/secrets/smtp", v1.SecretSpec{Value: ptr("x"), Type: "Shared"}, nil)
	x.must(http.StatusForbidden, x.b, http.MethodDelete, "/api/v1/namespaces/team-b/secrets/smtp?type=Shared", nil, nil)

	// Code: a tenant cannot point a workflow at the worker's disk, nor at a
	// bundle another tenant uploaded, even knowing its digest.
	x.must(http.StatusForbidden, x.b, http.MethodPut, "/api/v1/namespaces/team-b/workflows/Pipeline",
		v1.WorkflowSpec{Manifest: json.RawMessage(manifest), Source: v1.Source{LocalRoot: "/etc"}}, nil)
	var wf v1.Workflow
	x.must(http.StatusOK, x.a, http.MethodGet, "/api/v1/namespaces/team-a/workflows/Pipeline", nil, &wf)
	x.must(http.StatusUnprocessableEntity, x.b, http.MethodPut, "/api/v1/namespaces/team-b/workflows/Pipeline",
		v1.WorkflowSpec{Manifest: json.RawMessage(manifest), Source: v1.Source{BundleDigest: wf.Spec.Source.BundleDigest}}, nil)

	// A revoked token stops working at once.
	var toks v1.List[v1.AccessToken]
	x.must(http.StatusOK, x.b, http.MethodGet, "/api/v1/namespaces/team-b/tokens", nil, &toks)
	x.must(http.StatusNoContent, x.b, http.MethodDelete, "/api/v1/namespaces/team-b/tokens/"+toks.Items[0].ID, nil, nil)
	x.must(http.StatusUnauthorized, x.b, http.MethodGet, "/api/v1/whoami", nil, nil)
}

func TestTenantWorkerIsolation(t *testing.T) {
	x := startTenancy(t)
	digestA := x.deploy(x.a, "team-a", []byte("bundle-a"))
	x.deploy(x.b, "team-b", []byte("bundle-b"))

	// Each tenant brings its own workers; the admin runs a shared pool.
	credA, err := x.register(x.a, "/api/v1/namespaces/team-a/pools/a-pool/join-tokens", "wa")
	if err != nil {
		t.Fatal(err)
	}
	credB, err := x.register(x.b, "/api/v1/namespaces/team-b/pools/b-pool/join-tokens", "wb")
	if err != nil {
		t.Fatal(err)
	}
	credShared, err := x.register(adminToken, "/api/v1/pools/shared/join-tokens", "ws")
	if err != nil {
		t.Fatal(err)
	}

	// Pools and workers of one tenant are invisible to the other.
	var pools v1.List[v1.Pool]
	x.must(http.StatusOK, x.b, http.MethodGet, "/api/v1/namespaces/team-b/pools", nil, &pools)
	if len(pools.Items) != 1 || pools.Items[0].Metadata.Name != "b-pool" {
		t.Fatalf("team-b sees pools %+v", pools.Items)
	}
	var workers v1.List[v1.Worker]
	x.must(http.StatusOK, x.b, http.MethodGet, "/api/v1/namespaces/team-b/workers", nil, &workers)
	if len(workers.Items) != 1 || workers.Items[0].Metadata.Name != "wb" {
		t.Fatalf("team-b sees workers %+v", workers.Items)
	}
	x.must(http.StatusNotFound, x.b, http.MethodGet, "/api/v1/namespaces/team-b/pools/a-pool", nil, nil)
	x.must(http.StatusNotFound, x.b, http.MethodGet, "/api/v1/namespaces/team-b/workers/wa", nil, nil)
	x.must(http.StatusNotFound, x.b, http.MethodDelete, "/api/v1/namespaces/team-b/workers/wa", nil, nil)
	x.must(http.StatusNotFound, x.b, http.MethodPost, "/api/v1/namespaces/team-b/workers/wa/drain", nil, nil)
	x.must(http.StatusNotFound, x.b, http.MethodDelete, "/api/v1/namespaces/team-b/pools/a-pool", nil, nil)
	// Taking over another tenant's pool (to mint join tokens for it) fails.
	x.must(http.StatusConflict, x.b, http.MethodPut, "/api/v1/namespaces/team-b/pools/a-pool", v1.PoolSpec{}, nil)
	x.must(http.StatusConflict, x.b, http.MethodPost, "/api/v1/namespaces/team-b/pools/a-pool/join-tokens", v1.CreateJoinTokenRequest{}, nil)
	// ... and so does replacing its worker by registering under its name.
	if _, err := x.register(x.b, "/api/v1/namespaces/team-b/pools/b-pool/join-tokens", "wa"); err == nil {
		t.Fatal("team-b replaced team-a's worker by reusing its name")
	}
	x.must(http.StatusOK, adminToken, http.MethodGet, "/api/v1/workers", nil, &workers)
	if len(workers.Items) != 3 {
		t.Fatalf("admin sees %d workers, want 3", len(workers.Items))
	}

	// team-a's run only ever reaches team-a's worker.
	runA := x.run(x.a, "team-a")
	if got := x.poll(credB); len(got) != 0 {
		t.Fatalf("team-b's worker received team-a's work: %+v", got)
	}
	if got := x.poll(credShared); len(got) != 0 {
		t.Fatalf("a shared worker received work of a namespace that did not opt in: %+v", got)
	}
	got := x.poll(credA)
	if len(got) != 1 || got[0].RunId != runA {
		t.Fatalf("team-a's worker got %+v, want the step of %s", got, runA)
	}

	// A worker cannot fetch another tenant's code.
	fetch := func(cred, digest string) error {
		req := connect.NewRequest(&workerv1.FetchBundleRequest{Digest: digest})
		req.Header().Set("Authorization", "Bearer "+cred)
		_, err := x.worker.FetchBundle(context.Background(), req)
		return err
	}
	if err := fetch(credB, digestA); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("team-b's worker fetching team-a's bundle: %v, want NotFound", err)
	}
	if err := fetch(credA, digestA); err != nil {
		t.Fatalf("team-a's worker fetching its own bundle: %v", err)
	}

	// Shared workers serve a namespace once the admin opts it in.
	runB := x.run(x.b, "team-b")
	x.must(http.StatusOK, adminToken, http.MethodPut, "/api/v1/namespaces/team-b", v1.NamespaceSpec{SharedWorkers: true}, nil)
	if got := x.poll(credShared); len(got) != 1 || got[0].RunId != runB {
		t.Fatalf("shared worker got %+v, want the step of %s", got, runB)
	}
	x.must(http.StatusOK, x.b, http.MethodGet, "/api/v1/namespaces/team-b/pools", nil, &pools)
	if len(pools.Items) != 2 {
		t.Fatalf("team-b should now see the shared pool too: %+v", pools.Items)
	}
	// ... but still cannot manage them.
	x.must(http.StatusForbidden, x.b, http.MethodDelete, "/api/v1/namespaces/team-b/workers/ws", nil, nil)
	x.must(http.StatusForbidden, x.b, http.MethodPut, "/api/v1/namespaces/team-b/pools/shared", v1.PoolSpec{MaxConcurrentTasks: 1}, nil)
}

func TestNamespaceLifecycle(t *testing.T) {
	x := startTenancy(t)
	x.deploy(x.a, "team-a", []byte("bundle-a"))
	runA := x.run(x.a, "team-a")
	credA, err := x.register(x.a, "/api/v1/namespaces/team-a/pools/a-pool/join-tokens", "wa")
	if err != nil {
		t.Fatal(err)
	}

	// Namespaces are explicit: unknown ones are not created on first use.
	x.must(http.StatusNotFound, adminToken, http.MethodGet, "/api/v1/namespaces/nope/runs", nil, nil)
	x.must(http.StatusConflict, adminToken, http.MethodPost, "/api/v1/namespaces", v1.CreateNamespaceRequest{Name: "team-a"}, nil)
	x.must(http.StatusUnprocessableEntity, adminToken, http.MethodPost, "/api/v1/namespaces", v1.CreateNamespaceRequest{Name: "Bad Name"}, nil)
	x.must(http.StatusConflict, adminToken, http.MethodDelete, "/api/v1/namespaces/default", nil, nil)

	var ns v1.Namespace
	x.must(http.StatusOK, adminToken, http.MethodGet, "/api/v1/namespaces/team-a", nil, &ns)
	if ns.Status.Workflows != 1 || ns.Status.ActiveRuns != 1 || ns.Status.Workers != 1 || ns.Status.Tokens != 1 {
		t.Fatalf("status: %+v", ns.Status)
	}

	// Deleting a tenant removes everything it owned and revokes its access.
	x.must(http.StatusNoContent, adminToken, http.MethodDelete, "/api/v1/namespaces/team-a", nil, nil)
	x.must(http.StatusUnauthorized, x.a, http.MethodGet, "/api/v1/whoami", nil, nil)
	req := connect.NewRequest(&workerv1.PollRequest{MaxItems: 1, WaitSeconds: 1})
	req.Header().Set("Authorization", "Bearer "+credA)
	if _, err := x.worker.Poll(context.Background(), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("worker of a deleted namespace: %v, want Unauthenticated", err)
	}
	// Recreating the name starts empty.
	x.must(http.StatusCreated, adminToken, http.MethodPost, "/api/v1/namespaces", v1.CreateNamespaceRequest{Name: "team-a"}, nil)
	x.must(http.StatusNotFound, adminToken, http.MethodGet, "/api/v1/namespaces/team-a/runs/"+runA, nil, nil)
	var wfs v1.List[v1.Workflow]
	x.must(http.StatusOK, adminToken, http.MethodGet, "/api/v1/namespaces/team-a/workflows", nil, &wfs)
	var pools v1.List[v1.Pool]
	x.must(http.StatusOK, adminToken, http.MethodGet, "/api/v1/pools", nil, &pools)
	if len(wfs.Items) != 0 || len(pools.Items) != 0 {
		t.Fatalf("leftovers after delete: workflows=%d pools=%d", len(wfs.Items), len(pools.Items))
	}
}
