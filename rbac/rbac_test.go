package rbac

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestSet(t *testing.T) {
	s := NewSet("b", "a", "b")
	if !s.Has("a") || !s.Has("b") || s.Has("c") {
		t.Fatalf("Has wrong: %v", s.List())
	}
	if got := s.List(); !slices.Equal(got, []Permission{"a", "b"}) {
		t.Fatalf("List = %v", got)
	}
	l := s.List()
	l[0] = "z"
	if !s.Has("a") {
		t.Fatal("List must return a copy")
	}
	var zero Set
	if zero.Has("a") || zero.Len() != 0 {
		t.Fatal("zero Set must be empty")
	}
}

func TestRegistryResolve(t *testing.T) {
	r, err := NewRegistry(
		[]Permission{"todo.manage", "todo.read", "profile.edit_own"},
		[]Role{
			{Name: "admin", Permissions: []Permission{All}},
			{Name: "reader", Permissions: []Permission{"todo.read"}},
		},
		[]Permission{"profile.edit_own"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(nil).List(); !slices.Equal(got, []Permission{"profile.edit_own"}) {
		t.Fatalf("baseline only: %v", got)
	}
	if got := r.Resolve([]string{"reader", "ghost"}).List(); !slices.Equal(got, []Permission{"profile.edit_own", "todo.read"}) {
		t.Fatalf("reader: %v", got)
	}
	admin := r.Resolve([]string{"admin"})
	if admin.Len() != 3 || !admin.Has("todo.manage") || admin.Has(All) {
		t.Fatalf("admin should expand All: %v", admin.List())
	}
	if _, ok := r.Role("reader"); !ok {
		t.Fatal("Role(reader) missing")
	}
	if got := r.Expand([]Permission{All}).Len(); got != 3 {
		t.Fatalf("Expand(All) = %d perms", got)
	}
	if len(r.Roles()) != 2 || r.Roles()[0].Name != "admin" {
		t.Fatal("Roles order")
	}
}

func TestRegistryErrors(t *testing.T) {
	tests := []struct {
		name     string
		declared []Permission
		roles    []Role
		baseline []Permission
		want     string
	}{
		{"undeclared role perm", []Permission{"a"}, []Role{{Name: "r", Permissions: []Permission{"b"}}}, nil, `permission "b" is not declared`},
		{"undeclared baseline", []Permission{"a"}, nil, []Permission{"x"}, `baseline: permission "x"`},
		{"all in baseline", []Permission{"a"}, nil, []Permission{All}, "only allowed inside a role"},
		{"all declared", []Permission{All}, nil, nil, "cannot be declared"},
		{"duplicate role", nil, []Role{{Name: "r"}, {Name: "r"}}, nil, "defined twice"},
		{"empty role", nil, []Role{{Name: " "}}, nil, "empty name"},
		{"whitespace", []Permission{"a b"}, nil, nil, "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRegistry(tt.declared, tt.roles, tt.baseline)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestGrantFunc(t *testing.T) {
	var g GrantSource = GrantFunc(func(context.Context, string) ([]string, error) { return []string{"x"}, nil })
	roles, _ := g.RolesOf(context.Background(), "u")
	if len(roles) != 1 {
		t.Fatal(roles)
	}
}
