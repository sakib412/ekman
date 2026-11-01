// Package mail sends transactional email through a pluggable Sender.
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Message is one email.
type Message struct {
	To      []string
	Subject string
	Text    string // plain-text body
	HTML    string // HTML body; at least one of Text and HTML is required
	ReplyTo string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

func (m Message) validate() error {
	switch {
	case len(m.To) == 0:
		return errors.New("mail: no recipients")
	case strings.TrimSpace(m.Subject) == "":
		return errors.New("mail: empty subject")
	case m.Text == "" && m.HTML == "":
		return errors.New("mail: empty body")
	}
	return nil
}

// Log returns a Sender that logs messages instead of sending them, for development.
func Log(l *slog.Logger) Sender { return logSender{l} }

type logSender struct{ l *slog.Logger }

func (s logSender) Send(ctx context.Context, m Message) error {
	if err := m.validate(); err != nil {
		return err
	}
	s.l.InfoContext(ctx, "mail: not sent (log driver)", "to", m.To, "subject", m.Subject, "reply_to", m.ReplyTo, "text", m.Text, "html_bytes", len(m.HTML))
	return nil
}

const brevoEndpoint = "https://api.brevo.com/v3/smtp/email"

// Brevo returns a Sender for Brevo's transactional email HTTPS API (v3 /smtp/email). The free
// tier works with a verified single sender address as fromEmail.
func Brevo(apiKey, fromEmail, fromName string) Sender {
	return &brevo{
		apiKey: apiKey, from: brevoAddr{Email: fromEmail, Name: fromName},
		endpoint: brevoEndpoint, client: &http.Client{Timeout: 15 * time.Second},
	}
}

type brevo struct {
	apiKey   string
	from     brevoAddr
	endpoint string
	client   *http.Client
}

type brevoAddr struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoRequest struct {
	Sender      brevoAddr   `json:"sender"`
	To          []brevoAddr `json:"to"`
	Subject     string      `json:"subject"`
	HTMLContent string      `json:"htmlContent,omitempty"`
	TextContent string      `json:"textContent,omitempty"`
	ReplyTo     *brevoAddr  `json:"replyTo,omitempty"`
}

func (b *brevo) Send(ctx context.Context, m Message) error {
	if err := m.validate(); err != nil {
		return err
	}
	body := brevoRequest{Sender: b.from, Subject: m.Subject, HTMLContent: m.HTML, TextContent: m.Text}
	for _, to := range m.To {
		body.To = append(body.To, brevoAddr{Email: to})
	}
	if m.ReplyTo != "" {
		body.ReplyTo = &brevoAddr{Email: m.ReplyTo}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", b.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: brevo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mail: brevo: status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
