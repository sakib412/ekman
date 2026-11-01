// Package todo is a minimal ekman module: public listing and CSV export, permission-gated
// creation, and owner-or-manager actions including a file upload.
package todo

import (
	"context"
	"embed"
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/rbac"
	"github.com/sakib412/ekman/storage"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Manage lets a user create todos and complete anyone's.
const Manage rbac.Permission = "todo.manage"

// Service stores todos in Postgres and attachments in a bucket.
type Service struct {
	db    *db.DB
	files storage.Bucket
}

// New returns a Service backed by d, storing attachments in files.
func New(d *db.DB, files storage.Bucket) *Service { return &Service{db: d, files: files} }

// Priority orders todos. As an ekman.Enum it is a named string union in the OpenAPI document,
// and other values are rejected on input.
type Priority string

// The priorities.
const (
	Low    Priority = "low"
	Normal Priority = "normal"
	High   Priority = "high"
)

// EnumValues implements ekman.Enum.
func (Priority) EnumValues() []string { return []string{string(Low), string(Normal), string(High)} }

// Todo is one item.
type Todo struct {
	ID            int64     `json:"id"`
	Title         string    `json:"title"`
	OwnerID       string    `json:"ownerId"`
	Done          bool      `json:"done"`
	Priority      Priority  `json:"priority"`
	AttachmentURL *string   `json:"attachmentUrl"`
	CreatedAt     time.Time `json:"createdAt"`
}

const columns = `id, title, owner_id, done, priority, attachment_url, created_at`

func scan(row interface{ Scan(...any) error }, t *Todo) error {
	return row.Scan(&t.ID, &t.Title, &t.OwnerID, &t.Done, &t.Priority, &t.AttachmentURL, &t.CreatedAt)
}

// Module wires the service into an ekman app.
func (s *Service) Module() ekman.Module {
	return ekman.Module{
		Name:        "todo",
		Migrations:  migrations,
		Permissions: []rbac.Permission{Manage},
		Routes: func(r *ekman.Router) {
			ekman.Handle(r, "GET /todos", ekman.Public(), s.list, ekman.Summary("List todos"))
			r.Raw("GET /todos.csv", ekman.Public(), http.HandlerFunc(s.export),
				ekman.Summary("Export todos as CSV"), ekman.Produces[string]("text/csv"))
			ekman.Handle(r, "POST /todos", ekman.Requires(Manage), s.create, ekman.Summary("Create a todo"))
			ekman.Handle(r, "PUT /todos/{id}/done", ekman.SignedIn(), s.markDone, ekman.Summary("Mark a todo done"))
			ekman.Handle(r, "PUT /todos/{id}/attachment", ekman.SignedIn(), s.attach,
				ekman.Summary("Attach a PNG, JPEG or PDF"), ekman.Accepts[AttachmentForm]("multipart/form-data"))
		},
	}
}

// ListIn filters the list.
type ListIn struct {
	Done     *bool     `query:"done"`
	Priority *Priority `query:"priority"`
}

func (s *Service) list(ctx context.Context, _ ekman.Caller, in *ListIn) ([]Todo, error) {
	return s.query(ctx, in)
}

func (s *Service) query(ctx context.Context, in *ListIn) ([]Todo, error) {
	rows, err := s.db.Q(ctx).QueryContext(ctx,
		`SELECT `+columns+` FROM todos
		 WHERE ($1::boolean IS NULL OR done = $1) AND ($2::text IS NULL OR priority = $2) ORDER BY id`,
		in.Done, in.Priority)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Todo
	for rows.Next() {
		var t Todo
		if err := scan(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// export writes every todo as CSV. It is a Raw route because the response is not JSON; the
// Produces option documents it in the OpenAPI document.
func (s *Service) export(w http.ResponseWriter, r *http.Request) {
	todos, err := s.query(r.Context(), &ListIn{})
	if err != nil {
		ekman.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "title", "owner_id", "done", "priority", "created_at"})
	for _, t := range todos {
		_ = cw.Write([]string{strconv.FormatInt(t.ID, 10), t.Title, t.OwnerID, strconv.FormatBool(t.Done),
			string(t.Priority), t.CreatedAt.UTC().Format(time.RFC3339)})
	}
	cw.Flush()
}

// CreateIn is the body of POST /todos.
type CreateIn struct {
	Title    string   `json:"title"`
	Priority Priority `json:"priority,omitempty"` // default normal
}

// Validate implements ekman.Validator.
func (in *CreateIn) Validate(v *ekman.Validation) {
	v.Required("title", in.Title)
	v.MaxLen("title", in.Title, 200)
}

func (s *Service) create(ctx context.Context, c ekman.Caller, in *CreateIn) (Todo, error) {
	priority := in.Priority
	if priority == "" {
		priority = Normal
	}
	var t Todo
	err := scan(s.db.Q(ctx).QueryRowContext(ctx,
		`INSERT INTO todos (title, owner_id, priority) VALUES ($1, $2, $3) RETURNING `+columns,
		in.Title, c.UserID(), priority), &t)
	return t, err
}

// DoneIn identifies the todo to complete.
type DoneIn struct {
	ID int64 `path:"id"`
}

func (s *Service) markDone(ctx context.Context, c ekman.Caller, in *DoneIn) (ekman.NoContent, error) {
	return ekman.NoContent{}, s.db.Tx(ctx, func(ctx context.Context) error {
		if err := s.lockOwned(ctx, c, in.ID); err != nil {
			return err
		}
		_, err := s.db.Q(ctx).ExecContext(ctx, `UPDATE todos SET done = true WHERE id = $1`, in.ID)
		return err
	})
}

// lockOwned locks todo id inside a Tx and checks the caller owns it or holds Manage.
func (s *Service) lockOwned(ctx context.Context, c ekman.Caller, id int64) error {
	var owner string
	err := s.db.Q(ctx).QueryRowContext(ctx, `SELECT owner_id FROM todos WHERE id = $1 FOR UPDATE`, id).Scan(&owner)
	if err != nil {
		return err // sql.ErrNoRows becomes a 404
	}
	return c.RequireSelfOr(owner, Manage)
}

// AttachIn takes the request itself so storage.Receive can stream the multipart body; the id
// still binds from the path.
type AttachIn struct {
	ID  int64 `path:"id"`
	Req *http.Request
}

// AttachmentForm documents the multipart body of PUT /todos/{id}/attachment.
type AttachmentForm struct {
	File ekman.File `json:"file"`
}

var attachmentPolicy = storage.Policy{
	MaxBytes: 5 << 20,
	Types:    []string{"image/png", "image/jpeg", "application/pdf"},
	Prefix:   "todos/",
}

func (s *Service) attach(ctx context.Context, c ekman.Caller, in *AttachIn) (Todo, error) {
	// Check access before reading the body, and outside a transaction: the upload may be slow.
	var owner string
	err := s.db.Q(ctx).QueryRowContext(ctx, `SELECT owner_id FROM todos WHERE id = $1`, in.ID).Scan(&owner)
	if err != nil {
		return Todo{}, err
	}
	if err := c.RequireSelfOr(owner, Manage); err != nil {
		return Todo{}, err
	}
	obj, err := storage.Receive(in.Req, "file", s.files, attachmentPolicy)
	if err != nil {
		return Todo{}, err
	}
	var t Todo
	err = scan(s.db.Q(ctx).QueryRowContext(ctx,
		`UPDATE todos SET attachment_url = $2 WHERE id = $1 RETURNING `+columns, in.ID, obj.URL), &t)
	return t, err
}
