package ekman

import (
	"context"
	"testing"

	"github.com/sakib412/ekman/internal/testhook"
	"github.com/sakib412/ekman/rbac"
)

const (
	permManage rbac.Permission = "todo.manage"
	permEdit   rbac.Permission = "profile.edit_own"
)

type ownIn struct {
	Owner string `path:"owner"`
}

func accessModule() Module {
	ok := func(_ context.Context, c Caller, _ *struct{}) (Me, error) { return Me{UserID: c.UserID()}, nil }
	return Module{
		Name:        "todo",
		Permissions: []rbac.Permission{permManage, permEdit},
		Routes: func(r *Router) {
			Handle(r, "GET /public", Public(), ok)
			Handle(r, "GET /signed", SignedIn(), ok)
			Handle(r, "GET /manage", Requires(permManage), ok)
			Handle(r, "GET /own/{owner}", SignedIn(), func(_ context.Context, c Caller, in *ownIn) (NoContent, error) {
				return NoContent{}, c.RequireSelfOr(in.Owner, permManage)
			})
		},
	}
}

func TestAccess(t *testing.T) {
	_, h := newApp(t, Options{Auth: testAuth(), Baseline: []rbac.Permission{permEdit}}, accessModule())
	cases := []struct {
		name   string
		path   string
		id     *testhook.Identity
		status int
	}{
		{"public anon", "/api/v1/public", nil, 200},
		{"signed anon", "/api/v1/signed", nil, 401},
		{"signed user", "/api/v1/signed", as("u1"), 200},
		{"manage anon", "/api/v1/manage", nil, 401},
		{"manage without perm", "/api/v1/manage", as("u1"), 403},
		{"manage with perm", "/api/v1/manage", as("u1", permManage), 200},
		{"manage via All", "/api/v1/manage", as("u1", rbac.All), 200},
		{"self", "/api/v1/own/u1", as("u1"), 204},
		{"other", "/api/v1/own/u2", as("u1"), 403},
		{"other with perm", "/api/v1/own/u2", as("u1", permManage), 204},
		{"empty user is anonymous", "/api/v1/signed", as(""), 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectStatus(t, do(t, h, call{method: "GET", path: tc.path, as: tc.id}), tc.status)
		})
	}
}

func TestCaller(t *testing.T) {
	var anon Caller
	if !anon.Anonymous() || anon.Can(permManage) || anon.UserID() != "" {
		t.Fatal("zero Caller must be anonymous")
	}
	if e, _ := toError(anon.Require(permManage)); e.Status != 401 {
		t.Fatal(e)
	}
	c := Caller{userID: "u", perms: rbac.NewSet(permEdit)}
	if !c.Can(permEdit) || c.Can(permManage) {
		t.Fatal("Can")
	}
	if e, _ := toError(c.Require(permManage)); e.Status != 403 {
		t.Fatal(e)
	}
	if c.RequireSelfOr("u", permManage) != nil {
		t.Fatal("owner must pass")
	}
	if got := c.Permissions(); len(got) != 1 || got[0] != permEdit {
		t.Fatal(got)
	}
	ctx := withCaller(context.Background(), c)
	if CallerFrom(ctx).UserID() != "u" || !CallerFrom(context.Background()).Anonymous() {
		t.Fatal("CallerFrom")
	}
}

func TestAccessString(t *testing.T) {
	if Public().String() != "public" || SignedIn().String() != "signed_in" || Requires("a.b").String() != "a.b" || (Access{}).String() != "invalid" {
		t.Fatal("Access.String")
	}
}
