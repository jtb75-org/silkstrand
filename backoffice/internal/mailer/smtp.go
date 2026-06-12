package mailer

import (
	"fmt"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTP sends transactional email through a plain in-cluster SMTP relay — the
// Postfix -> AWS SES mail-relay. SilkStrand holds no SES credentials: it just
// hands the message to the relay (which is restricted to the cluster network),
// and the relay authenticates to SES and applies its sender-domain allowlist.
type SMTP struct {
	Addr      string // host:port, e.g. mail-relay.mail.svc.cluster.local:25
	FromEmail string // e.g. "SilkStrand <noreply@silkstrand.io>"
}

func NewSMTP(addr, fromEmail string) *SMTP {
	return &SMTP{Addr: addr, FromEmail: fromEmail}
}

func (s *SMTP) SendInvite(to, inviteURL, tenantName string) error {
	subject, text, html := inviteEmail(inviteURL, tenantName)
	return s.send(to, subject, text, html)
}

func (s *SMTP) SendPasswordReset(to, resetURL string) error {
	subject, text, html := passwordResetEmail(resetURL)
	return s.send(to, subject, text, html)
}

func (s *SMTP) send(to, subject, text, html string) error {
	from, err := mail.ParseAddress(s.FromEmail)
	if err != nil {
		return fmt.Errorf("parsing from address %q: %w", s.FromEmail, err)
	}
	msg := buildMIME(s.FromEmail, to, subject, text, html)

	// Plain SMTP to the in-cluster relay: no auth, no STARTTLS. The relay only
	// accepts mail from the cluster network (Postfix mynetworks) and does the
	// SES submission (with TLS + creds) itself.
	c, err := smtp.Dial(s.Addr)
	if err != nil {
		return fmt.Errorf("dialing smtp relay %s: %w", s.Addr, err)
	}
	defer c.Close()
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO %s: %w", to, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp writing body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp closing body: %w", err)
	}
	return c.Quit()
}

// buildMIME assembles an RFC 5322 multipart/alternative message (plain text +
// HTML) with CRLF line endings, as SMTP requires.
func buildMIME(from, to, subject, text, html string) string {
	const boundary = "_=_silkstrand_alt_boundary_=_"
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString(`Content-Type: multipart/alternative; boundary="` + boundary + "\"\r\n")
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(toCRLF(text) + "\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(toCRLF(html) + "\r\n")

	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

// toCRLF normalizes bare LF line endings to CRLF for SMTP transport.
func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
