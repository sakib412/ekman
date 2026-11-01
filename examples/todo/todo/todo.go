// Package todo is a minimal ekman module: public listing, permission-gated creation, and an
// owner-or-manager action.
package todo

import (
	"context"
	"embed"
	"time"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/rbac"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Manage lets a user create todos and complete anyone's.
const Manage rbac.Permission = "todo.manage"

// Service stores todos in Postgres.
type Service struct{ db *db.DB }

// New returns a Service backed by d.
func New(d *db.DB) *Service { return &Service{db: d} }

// Todo is one item.
type Todo struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	OwnerID   string    `json:"ownerId"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"createdAt"`
}

// Module wires the service into an ekman app.
func (s *Service) Module() ekman.Module {
	return ekman.Module{
		Name:        "todo",
		Migrations:  migrations,
		Permissions: []rbac.Permission{Manage},
		Routes: func(r *ekman.Router) {
			ekman.Handle(r, "GET /todos", ekman.Public(), s.list, ekman.Summary("List todos"))
			ekman.Handle(r, "POST /todos", ekman.Requires(Manage), s.create, ekman.Summary("Create a todo"))
			ekman.Handle(r, "PUT /todos/{id}/done", ekman.SignedIn(), s.markDone, ekman.Summary("Mark a todo done"))
		},
	}
}

// ListIn filters the list.
type ListIn struct {
	Done *bool `query:"done"`
}

func (s *Service) list(ctx context.Context, _ ekman.Caller, in *ListIn) ([]Todo, error) {
	rows, err := s.db.Q(ctx).QueryContext(ctx,
		`SELECT id, title, owner_id, done, created_at FROM todos
		 WHERE $1::boolean IS NULL OR done = $1 ORDER BY id`, in.Done)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Todo
	for rows.Next() {
		var t Todo
		if err := rows.Scan(&t.ID, &t.Title, &t.OwnerID, &t.Done, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateIn is the body of POST /todos.
type CreateIn struct {
	Title string `json:"title"`
}

// Validate implements ekman.Validator.
func (in *CreateIn) Validate(v *ekman.Validation) {
	v.Required("title", in.Title)
	v.MaxLen("title", in.Title, 200)
}

func (s *Service) create(ctx context.Context, c ekman.Caller, in *CreateIn) (Todo, error) {
	t := Todo{Title: in.Title, OwnerID: c.UserID()}
	err := s.db.Q(ctx).QueryRowContext(ctx,
		`INSERT INTO todos (title, owner_id) VALUES ($1, $2) RETURNING id, done, created_at`,
		t.Title, t.OwnerID).Scan(&t.ID, &t.Done, &t.CreatedAt)
	return t, err
}

// DoneIn identifies the todo to complete.
type DoneIn struct {
	ID int64 `path:"id"`
}

func (s *Service) markDone(ctx context.Context, c ekman.Caller, in *DoneIn) (ekman.NoContent, error) {
	return ekman.NoContent{}, s.db.Tx(ctx, func(ctx context.Context) error {
		var owner string
		err := s.db.Q(ctx).QueryRowContext(ctx, `SELECT owner_id FROM todos WHERE id = $1 FOR UPDATE`, in.ID).Scan(&owner)
		if err != nil {
			return err // sql.ErrNoRows becomes a 404
		}
		if err := c.RequireSelfOr(owner, Manage); err != nil {
			return err
		}
		_, err = s.db.Q(ctx).ExecContext(ctx, `UPDATE todos SET done = true WHERE id = $1`, in.ID)
		return err
	})
}
