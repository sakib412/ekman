// Command todo is a runnable ekman example.
//
//	DATABASE_URL=postgres://... go run . migrate
//	DATABASE_URL=postgres://... go run .            # serve on :8080 (UPLOAD_DIR, UPLOAD_BASE_URL)
//	go run . openapi > openapi.json
package main

import (
	"context"
	"log"
	"os"
	"slices"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // the app brings the driver; ekman imports none

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/examples/todo/todo"
	"github.com/sakib412/ekman/rbac"
	"github.com/sakib412/ekman/storage"
)

func main() {
	database, err := db.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	uploads := os.Getenv("UPLOAD_DIR")
	if uploads == "" {
		uploads = "uploads"
	}
	todos := todo.New(database, storage.Dir(uploads, os.Getenv("UPLOAD_BASE_URL")))
	publicURL := os.Getenv("PUBLIC_URL")

	app, err := ekman.New(ekman.Options{
		DB:     database,
		Roles:  []rbac.Role{{Name: "admin", Permissions: []rbac.Permission{rbac.All}}},
		Grants: admins(strings.Split(os.Getenv("ADMIN_USER_IDS"), ",")),
		Auth: &auth.Config{
			Providers: providers(),
			Directory: auth.DirectoryFunc(directory),
			PublicURL: publicURL,
		},
		CORS: nonEmpty(strings.Split(os.Getenv("CORS_ORIGINS"), ",")),
		Info: ekman.Info{Title: "Todo API", Version: "0.2.0"},
		// Anything ekman does not derive can be added to the document here.
		OpenAPIHook: func(doc map[string]any) error {
			if publicURL != "" {
				doc["servers"] = []any{map[string]any{"url": publicURL}}
			}
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	app.Register(todos.Module())
	app.Main()
}

func providers() []auth.Provider {
	var ps []auth.Provider
	if id := os.Getenv("GOOGLE_CLIENT_ID"); id != "" {
		ps = append(ps, auth.Google(id, os.Getenv("GOOGLE_CLIENT_SECRET")))
	}
	if id := os.Getenv("GITHUB_CLIENT_ID"); id != "" {
		ps = append(ps, auth.GitHub(id, os.Getenv("GITHUB_CLIENT_SECRET")))
	}
	return ps
}

// directory lets anyone with a verified email in; the user id is provider-scoped.
func directory(_ context.Context, id auth.Identity) (string, error) {
	if !id.EmailVerified {
		return "", ekman.Forbidden("a verified email address is required")
	}
	return id.Provider + ":" + id.Subject, nil
}

// admins grants the admin role to the listed user ids.
func admins(ids []string) rbac.GrantSource {
	return rbac.GrantFunc(func(_ context.Context, userID string) ([]string, error) {
		if slices.Contains(ids, userID) {
			return []string{"admin"}, nil
		}
		return nil, nil
	})
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
