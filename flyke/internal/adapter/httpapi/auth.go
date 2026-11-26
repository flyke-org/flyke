package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
	"github.com/flyke-org/flyke/flyke/internal/domain/tenant"
)

// principal is the authenticated caller.
//
//   - The admin (the control plane's admin token, or anyone when no admin
//     token is configured) can use every route and every namespace.
//   - A tenant (an access token) can only use /api/v1/namespaces/<its
//     namespace>/… ; everything else answers 403.
type principal struct {
	admin     bool
	namespace string // the tenant's namespace (empty for the admin)
	token     string // access token name
}

type principalKey struct{}

func caller(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

// scope is the namespace a use case is called for: the path's namespace, or
// "" on cluster routes (admin only), which use cases read as "all".
func scope(r *http.Request) string { return r.PathValue("ns") }

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.identify(r)
		if !ok {
			writeStatus(w, http.StatusUnauthorized, "Unauthorized", "missing or invalid API token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (a *API) identify(r *http.Request) (principal, bool) {
	if a.adminToken == "" {
		return principal{admin: true}, true // open control plane (development)
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tenant.IsAccessToken(got) {
		tok, err := a.svc.AuthenticateAccessToken(r.Context(), got)
		if err != nil {
			return principal{}, false
		}
		return principal{namespace: tok.Namespace, token: tok.Name}, true
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.adminToken)) == 1 {
		return principal{admin: true}, true
	}
	return principal{}, false
}

type access int

const (
	// tenantAccess: the admin, or a tenant inside its own namespace. On
	// routes without a namespace it means admin only.
	tenantAccess access = iota
	// adminAccess: the admin only, even on a namespaced route.
	adminAccess
	// anyAccess: any authenticated caller.
	anyAccess
)

// guard is the single authorization point of the REST API: every route is
// registered through it, so a new route is closed to tenants by default.
func (a *API) guard(level access, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ns := caller(r), r.PathValue("ns")
		switch {
		case level == anyAccess || p.admin:
		case level == adminAccess || ns == "":
			writeStatus(w, http.StatusForbidden, "Forbidden", "this operation needs the admin token")
			return
		case ns != p.namespace:
			// Same answer whether or not the namespace exists.
			writeStatus(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("this token cannot access namespace %q", ns))
			return
		}
		if ns != "" && level != anyAccess {
			ok, err := a.svc.NamespaceExists(r.Context(), ns)
			if err != nil {
				a.fail(w, err)
				return
			}
			if !ok && p.admin && a.svc.Config().Dev && r.Method != http.MethodGet {
				// Dev control planes are single-user and often ephemeral
				// (`flyke run -n scratch …`): create namespaces on first write.
				_, err := a.svc.CreateNamespace(r.Context(), tenant.Namespace{Name: ns})
				ok = err == nil || errors.Is(err, usecase.ErrAlreadyExists)
			}
			if !ok {
				writeStatus(w, http.StatusNotFound, "NotFound",
					fmt.Sprintf("namespace %q does not exist; an admin can create it with `flyke namespaces create %s`", ns, ns))
				return
			}
		}
		h(w, r)
	}
}

func (a *API) whoami(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	writeJSON(w, http.StatusOK, v1.WhoAmI{
		TypeMeta: tm("WhoAmI"), Admin: p.admin, Namespace: p.namespace, Token: p.token, AuthRequired: a.adminToken != "",
	})
}

// ------------------------------------------------------------ namespaces ----

func namespaceDTO(n usecase.NamespaceInfo) v1.Namespace {
	return v1.Namespace{
		TypeMeta: tm("Namespace"),
		Metadata: v1.ObjectMeta{Name: n.Name, CreatedAt: n.CreatedAt},
		Spec:     v1.NamespaceSpec{Description: n.Description, SharedWorkers: n.SharedWorkers},
		Status: v1.NamespaceStatus{
			Workflows: n.Stats.Workflows, ActiveRuns: n.Stats.ActiveRuns, Workers: n.Stats.Workers, Tokens: n.Stats.Tokens,
		},
	}
}

// listNamespaces returns every namespace to the admin, and to a tenant only
// its own.
func (a *API) listNamespaces(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	var out []v1.Namespace
	if p.admin {
		all, err := a.svc.ListNamespaces(r.Context())
		if err != nil {
			a.fail(w, err)
			return
		}
		for _, n := range all {
			out = append(out, namespaceDTO(n))
		}
	} else if n, err := a.svc.GetNamespace(r.Context(), p.namespace); err == nil {
		out = append(out, namespaceDTO(n))
	}
	writeJSON(w, http.StatusOK, list("Namespace", out))
}

func (a *API) createNamespace(w http.ResponseWriter, r *http.Request) {
	var body v1.CreateNamespaceRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	n, err := a.svc.CreateNamespace(r.Context(), tenant.Namespace{
		Name: body.Name, Description: body.Description, SharedWorkers: body.SharedWorkers,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, namespaceDTO(usecase.NamespaceInfo{Namespace: n}))
}

func (a *API) getNamespace(w http.ResponseWriter, r *http.Request) {
	n, err := a.svc.GetNamespace(r.Context(), scope(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, namespaceDTO(n))
}

func (a *API) putNamespace(w http.ResponseWriter, r *http.Request) {
	var body v1.NamespaceSpec
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	if _, err := a.svc.UpdateNamespace(r.Context(), tenant.Namespace{
		Name: scope(r), Description: body.Description, SharedWorkers: body.SharedWorkers,
	}); err != nil {
		a.fail(w, err)
		return
	}
	a.getNamespace(w, r)
}

func (a *API) deleteNamespace(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteNamespace(r.Context(), scope(r)); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------- access tokens ----

func accessTokenDTO(t tenant.AccessToken, plain string) v1.AccessToken {
	return v1.AccessToken{ID: t.ID, Name: t.Name, Namespace: t.Namespace, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, Token: plain}
}

func (a *API) listAccessTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := a.svc.ListAccessTokens(r.Context(), scope(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.AccessToken, len(toks))
	for i, t := range toks {
		out[i] = accessTokenDTO(t, "")
	}
	writeJSON(w, http.StatusOK, list("AccessToken", out))
}

func (a *API) createAccessToken(w http.ResponseWriter, r *http.Request) {
	var body v1.CreateAccessTokenRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	if body.TTLSeconds < 0 {
		a.fail(w, fmt.Errorf("%w: ttlSeconds must not be negative", usecase.ErrInvalidArgument))
		return
	}
	tok, plain, err := a.svc.CreateAccessToken(r.Context(), scope(r), body.Name, time.Duration(body.TTLSeconds)*time.Second)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, accessTokenDTO(tok, plain))
}

func (a *API) deleteAccessToken(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteAccessToken(r.Context(), scope(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
