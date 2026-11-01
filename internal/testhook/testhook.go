// Package testhook lets ekmantest hand ekman a caller identity without OAuth. It is internal,
// so only packages inside the ekman module can set the context value; applications cannot
// use it to bypass sign-in.
package testhook

import (
	"context"

	"github.com/sakib412/ekman/rbac"
)

// Identity is the caller ekman should use for a request.
type Identity struct {
	UserID string
	Perms  []rbac.Permission
}

type key struct{}

// With returns ctx carrying id.
func With(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// From returns the identity in ctx, if any.
func From(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(key{}).(Identity)
	return id, ok
}
