// Package rbac holds ekman's role-based access control primitives: permissions declared by
// modules, roles that bundle them, and the registry that resolves role names to a permission set.
package rbac

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Permission names one thing a caller may do, such as "todo.manage".
type Permission string

// All grants every declared permission. It is only valid inside Role.Permissions.
const All Permission = "*"

// Role bundles permissions under a name that grant sources refer to.
type Role struct {
	Name        string
	Title       string
	Permissions []Permission
}

// GrantSource reports which roles a signed-in user holds.
type GrantSource interface {
	RolesOf(ctx context.Context, userID string) ([]string, error)
}

// GrantFunc adapts a function to GrantSource.
type GrantFunc func(ctx context.Context, userID string) ([]string, error)

// RolesOf calls f.
func (f GrantFunc) RolesOf(ctx context.Context, userID string) ([]string, error) {
	return f(ctx, userID)
}

// Set is an immutable set of permissions. The zero value is empty.
type Set struct {
	sorted []Permission
}

// NewSet builds a Set from perms, dropping duplicates. All is kept literally; expansion is
// the Registry's job.
func NewSet(perms ...Permission) Set {
	s := slices.Clone(perms)
	slices.Sort(s)
	return Set{sorted: slices.Compact(s)}
}

// Has reports whether p is in the set.
func (s Set) Has(p Permission) bool {
	_, ok := slices.BinarySearch(s.sorted, p)
	return ok
}

// List returns the permissions in sorted order. The slice is a copy.
func (s Set) List() []Permission {
	return slices.Clone(s.sorted)
}

// Len returns the number of permissions in the set.
func (s Set) Len() int { return len(s.sorted) }

// Registry knows every declared permission, the configured roles and the baseline every
// signed-in user holds. It is immutable after NewRegistry.
type Registry struct {
	declared Set
	roles    []Role
	byName   map[string]Set
	baseline Set
}

// NewRegistry validates roles and baseline against the declared permissions. A role or
// baseline naming an undeclared permission is an error, so typos fail at boot.
func NewRegistry(declared []Permission, roles []Role, baseline []Permission) (*Registry, error) {
	var errs []error
	for _, p := range declared {
		if err := checkName(p); err != nil {
			errs = append(errs, fmt.Errorf("declared permission: %w", err))
		}
	}
	decl := NewSet(declared...)
	r := &Registry{declared: decl, byName: make(map[string]Set, len(roles))}
	for _, role := range roles {
		if strings.TrimSpace(role.Name) == "" {
			errs = append(errs, errors.New("role with empty name"))
			continue
		}
		if _, dup := r.byName[role.Name]; dup {
			errs = append(errs, fmt.Errorf("role %q defined twice", role.Name))
			continue
		}
		var perms []Permission
		for _, p := range role.Permissions {
			switch {
			case p == All:
				perms = append(perms, decl.sorted...)
			case !decl.Has(p):
				errs = append(errs, fmt.Errorf("role %q: permission %q is not declared by any module", role.Name, p))
			default:
				perms = append(perms, p)
			}
		}
		r.byName[role.Name] = NewSet(perms...)
		role.Permissions = slices.Clone(role.Permissions)
		r.roles = append(r.roles, role)
	}
	for _, p := range baseline {
		switch {
		case p == All:
			errs = append(errs, errors.New("baseline: rbac.All is only allowed inside a role"))
		case !decl.Has(p):
			errs = append(errs, fmt.Errorf("baseline: permission %q is not declared by any module", p))
		}
	}
	r.baseline = NewSet(baseline...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return r, nil
}

func checkName(p Permission) error {
	switch {
	case p == "":
		return errors.New("empty permission")
	case p == All:
		return errors.New("rbac.All cannot be declared; use it inside a role")
	case strings.ContainsFunc(string(p), func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }):
		return fmt.Errorf("permission %q contains whitespace", p)
	}
	return nil
}

// Resolve returns the baseline plus the permissions of the named roles. Unknown role names are
// ignored; callers that want to report them use Role.
func (r *Registry) Resolve(roleNames []string) Set {
	perms := slices.Clone(r.baseline.sorted)
	for _, name := range roleNames {
		if s, ok := r.byName[name]; ok {
			perms = append(perms, s.sorted...)
		}
	}
	return NewSet(perms...)
}

// Expand replaces All in perms with every declared permission.
func (r *Registry) Expand(perms []Permission) Set {
	out := make([]Permission, 0, len(perms))
	for _, p := range perms {
		if p == All {
			out = append(out, r.declared.sorted...)
			continue
		}
		out = append(out, p)
	}
	return NewSet(out...)
}

// Role returns the role with the given name.
func (r *Registry) Role(name string) (Role, bool) {
	for _, role := range r.roles {
		if role.Name == name {
			return role, true
		}
	}
	return Role{}, false
}

// Declared reports whether p was declared by a module.
func (r *Registry) Declared(p Permission) bool { return r.declared.Has(p) }

// Permissions returns every declared permission, sorted.
func (r *Registry) Permissions() []Permission { return r.declared.List() }

// Baseline returns the permissions every signed-in user holds, sorted.
func (r *Registry) Baseline() []Permission { return r.baseline.List() }

// Roles returns the configured roles in their original order.
func (r *Registry) Roles() []Role {
	out := make([]Role, len(r.roles))
	for i, role := range r.roles {
		role.Permissions = slices.Clone(role.Permissions)
		out[i] = role
	}
	return out
}
