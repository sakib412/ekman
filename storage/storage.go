// Package storage stores uploaded files in a Bucket and receives multipart uploads safely.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Object is a stored file.
type Object struct {
	Key         string
	URL         string
	ContentType string
	Size        int64
}

// Bucket stores objects by key.
type Bucket interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error)
	// Delete removes key; deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
}

// Dir stores objects as files under root. URLs are publicBaseURL + "/" + key; serve root at
// that URL yourself (e.g. with http.FileServer or a CDN).
func Dir(root, publicBaseURL string) Bucket {
	return &dir{root: root, base: strings.TrimSuffix(publicBaseURL, "/")}
}

type dir struct {
	root string
	base string
}

// ErrInvalidKey is returned for keys that are empty, absolute or escape the bucket.
var ErrInvalidKey = errors.New("storage: invalid key")

func (d *dir) path(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || path.Clean(key) != key || strings.HasPrefix(key, "../") || key == ".." {
		return "", ErrInvalidKey
	}
	return filepath.Join(d.root, filepath.FromSlash(key)), nil
}

func (d *dir) Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error) {
	p, err := d.path(key)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Object{}, fmt.Errorf("storage: %w", err)
	}
	// Write to a temp file and rename, so readers never see a partial object.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return Object{}, fmt.Errorf("storage: %w", err)
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, ctxReader{ctx, r})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Object{}, err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return Object{}, fmt.Errorf("storage: %w", err)
	}
	return Object{Key: key, URL: d.url(key), ContentType: contentType, Size: n}, nil
}

func (d *dir) url(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return d.base + "/" + strings.Join(segs, "/")
}

func (d *dir) Delete(_ context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: %w", err)
	}
	return nil
}

// ctxReader stops a long copy when the request is canceled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
