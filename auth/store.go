package auth

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"sync"
	"time"

	"github.com/sakib412/ekman/db"
)

// ErrNoSession is returned by Store.Get for a missing or expired session.
var ErrNoSession = errors.New("auth: no session")

// Session is a signed-in browser. Only the SHA-256 of the cookie token is stored.
type Session struct {
	TokenHash []byte
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store persists sessions.
type Store interface {
	Create(ctx context.Context, s Session) error
	// Get returns ErrNoSession when the session does not exist or has expired.
	Get(ctx context.Context, tokenHash []byte) (Session, error)
	Touch(ctx context.Context, tokenHash []byte, expires time.Time) error
	Delete(ctx context.Context, tokenHash []byte) error
	// DeleteUser signs a user out everywhere.
	DeleteUser(ctx context.Context, userID string) error
}

// MemoryStore keeps sessions in process memory: for tests and single-process development.
func MemoryStore() Store { return &memoryStore{m: map[string]Session{}} }

type memoryStore struct {
	mu sync.Mutex
	m  map[string]Session
}

func (s *memoryStore) Create(_ context.Context, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(sess.TokenHash)] = sess
	return nil
}

func (s *memoryStore) Get(_ context.Context, h []byte) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[string(h)]
	if !ok || !time.Now().Before(sess.ExpiresAt) {
		return Session{}, ErrNoSession
	}
	return sess, nil
}

func (s *memoryStore) Touch(_ context.Context, h []byte, exp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.m[string(h)]; ok {
		sess.ExpiresAt = exp
		s.m[string(h)] = sess
	}
	return nil
}

func (s *memoryStore) Delete(_ context.Context, h []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, string(h))
	return nil
}

func (s *memoryStore) DeleteUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, sess := range s.m {
		if sess.UserID == userID {
			delete(s.m, k)
		}
	}
	return nil
}

//go:embed migrations/*.sql
var migrations embed.FS

// PostgresStore keeps sessions in the ekman_sessions table. ekman applies the table's
// migration (module "ekman") when the store is in use.
func PostgresStore(d *db.DB) Store { return &pgStore{db: d} }

type pgStore struct{ db *db.DB }

// Migrations is discovered by ekman through an interface assertion so the schema ships with
// the store without widening the package API.
func (s *pgStore) Migrations() fs.FS { return migrations }

func (s *pgStore) Create(ctx context.Context, sess Session) error {
	q := s.db.Q(ctx)
	if _, err := q.ExecContext(ctx, `DELETE FROM ekman_sessions WHERE user_id = $1 AND expires_at < now()`, sess.UserID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		`INSERT INTO ekman_sessions (token_hash, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`,
		sess.TokenHash, sess.UserID, sess.CreatedAt, sess.ExpiresAt)
	return err
}

func (s *pgStore) Get(ctx context.Context, h []byte) (Session, error) {
	var sess Session
	err := s.db.Q(ctx).QueryRowContext(ctx,
		`SELECT token_hash, user_id, created_at, expires_at FROM ekman_sessions WHERE token_hash = $1`, h).
		Scan(&sess.TokenHash, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	if !time.Now().Before(sess.ExpiresAt) {
		return Session{}, ErrNoSession
	}
	return sess, nil
}

func (s *pgStore) Touch(ctx context.Context, h []byte, exp time.Time) error {
	_, err := s.db.Q(ctx).ExecContext(ctx, `UPDATE ekman_sessions SET expires_at = $2 WHERE token_hash = $1`, h, exp)
	return err
}

func (s *pgStore) Delete(ctx context.Context, h []byte) error {
	_, err := s.db.Q(ctx).ExecContext(ctx, `DELETE FROM ekman_sessions WHERE token_hash = $1`, h)
	return err
}

func (s *pgStore) DeleteUser(ctx context.Context, userID string) error {
	_, err := s.db.Q(ctx).ExecContext(ctx, `DELETE FROM ekman_sessions WHERE user_id = $1`, userID)
	return err
}
