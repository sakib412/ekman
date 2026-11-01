package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/sakib412/ekman"
)

// Policy limits what Receive accepts.
type Policy struct {
	MaxBytes int64    // per file; default 10 MiB
	Types    []string // allowed sniffed content types, e.g. "image/png"; required
	Prefix   string   // key prefix, e.g. "avatars/"
}

const defaultMaxBytes = 10 << 20

// Receive streams one multipart file field into b. It enforces MaxBytes, sniffs the real
// content type with http.DetectContentType (the client's claimed type is ignored), rejects
// types not in Policy.Types, and stores the file under Prefix with a random key. Errors are
// *ekman.Error values (400, 413, 415) ready to return from a handler.
func Receive(r *http.Request, field string, b Bucket, p Policy) (Object, error) {
	if len(p.Types) == 0 {
		return Object{}, errors.New("storage: Policy.Types must list the allowed content types")
	}
	max := p.MaxBytes
	if max <= 0 {
		max = defaultMaxBytes
	}
	// Bound the whole body too: multipart headers and other fields ride along.
	r.Body = http.MaxBytesReader(nil, r.Body, max+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return Object{}, ekman.BadRequest("invalid_upload", "expected a multipart/form-data body")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return Object{}, ekman.BadRequest("missing_file", "no file in field "+field)
		}
		if err != nil {
			return Object{}, uploadErr(err)
		}
		if part.FormName() != field || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		defer part.Close()
		return store(r, part, b, p, max)
	}
}

func store(r *http.Request, part io.Reader, b Bucket, p Policy, max int64) (Object, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Object{}, uploadErr(err)
	}
	head = head[:n]
	if n == 0 {
		return Object{}, ekman.BadRequest("empty_file", "the uploaded file is empty")
	}
	ct, _, _ := mime.ParseMediaType(http.DetectContentType(head))
	if !slices.Contains(p.Types, ct) {
		return Object{}, &ekman.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_type",
			Message: "file type " + ct + " is not allowed; allowed: " + strings.Join(p.Types, ", ")}
	}
	lr := &limitReader{r: io.MultiReader(bytes.NewReader(head), part), left: max}
	key := p.Prefix + randomName() + extension(ct)
	obj, err := b.Put(r.Context(), key, lr, ct)
	if lr.exceeded {
		return Object{}, &ekman.Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "file exceeds the size limit"}
	}
	if err != nil {
		return Object{}, uploadErr(err)
	}
	return obj, nil
}

func uploadErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return &ekman.Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "upload too large"}
	}
	return err
}

var errTooLarge = errors.New("storage: file exceeds the size limit")

// limitReader fails, rather than truncating, once more than left bytes are read.
type limitReader struct {
	r        io.Reader
	left     int64
	exceeded bool
}

func (l *limitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		l.exceeded = true
		return 0, errTooLarge
	}
	return n, err
}

func randomName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func extension(ct string) string {
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	}
	return ""
}
