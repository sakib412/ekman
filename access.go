package ekman

import (
	"context"
	"slices"

	"github.com/sakib412/ekman/rbac"
)

type accessKind uint8

const (
	accessInvalid accessKind = iota
	accessPublic
	accessSignedIn
	accessRequires
)

// Access states who may call a route. Every route passes one explicitly; the zero value is
// rejected at boot so nothing defaults open.
type Access struct {
	kind accessKind
	perm rbac.Permission
}

// Public lets anyone call the route; the Caller may be anonymous.
func Public() Access { return Access{kind: accessPublic} }

// SignedIn requires a session: anonymous callers get 401.
func SignedIn() Access { return Access{kind: accessSignedIn} }

// Requires requires a session holding p: anonymous callers get 401, others without p get 403.
func Requires(p rbac.Permission) Access { return Access{kind: accessRequires, perm: p} }

// String returns "public", "signed_in", or the required permission.
func (a Access) String() string {
	switch a.kind {
	case accessPublic:
		return "public"
	case accessSignedIn:
		return "signed_in"
	case accessRequires:
		return string(a.perm)
	}
	return "invalid"
}

func (a Access) check(c Caller) error {
	switch a.kind {
	case accessPublic:
		return nil
	case accessSignedIn:
		if c.Anonymous() {
			return Unauthorized("sign in required")
		}
		return nil
	case accessRequires:
		return c.Require(a.perm)
	}
	return Forbidden("route has no access rule")
}

// Caller is who is making the request. ekman builds it from the session; handlers receive it
// as an argument and middleware reads it with CallerFrom.
type Caller struct {
	userID string
	perms  rbac.Set
	roles  []string
}

// Anonymous reports whether the request has no session.
func (c Caller) Anonymous() bool { return c.userID == "" }

// UserID returns the signed-in user's id, or "" when anonymous.
func (c Caller) UserID() string { return c.userID }

// Can reports whether the caller holds p. Anonymous callers hold nothing.
func (c Caller) Can(p rbac.Permission) bool { return !c.Anonymous() && c.perms.Has(p) }

// Permissions returns the caller's permissions, sorted.
func (c Caller) Permissions() []rbac.Permission { return c.perms.List() }

// Require returns a 401 for anonymous callers, a 403 when p is missing, else nil.
func (c Caller) Require(p rbac.Permission) error {
	if c.Anonymous() {
		return Unauthorized("sign in required")
	}
	if !c.perms.Has(p) {
		return Forbidden("missing permission " + string(p))
	}
	return nil
}

// RequireSelfOr allows the owner of a resource, or anyone holding p.
func (c Caller) RequireSelfOr(owner string, p rbac.Permission) error {
	if !c.Anonymous() && c.userID == owner {
		return nil
	}
	return c.Require(p)
}

func (c Caller) roleNames() []string { return slices.Clone(c.roles) }

type callerKey struct{}

// CallerFrom returns the caller for a request context; anonymous when there is none.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

func withCaller(ctx context.Context, c Caller) context.Context {
	if st := stateFrom(ctx); st != nil {
		st.userID = c.userID
	}
	return context.WithValue(ctx, callerKey{}, c)
}
