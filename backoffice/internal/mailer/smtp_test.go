package mailer

import (
	"strings"
	"testing"
)

func TestToCRLF(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\nb", "a\r\nb"},
		{"a\r\nb", "a\r\nb"}, // already CRLF, unchanged
		{"a\r\n\nb", "a\r\n\r\nb"},
		{"no newline", "no newline"},
	}
	for _, tt := range tests {
		if got := toCRLF(tt.in); got != tt.want {
			t.Errorf("toCRLF(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildMIME(t *testing.T) {
	msg := buildMIME("SilkStrand <noreply@silkstrand.io>", "u@example.com",
		"Hello", "plain body\nline2", "<p>html body</p>")

	must := []string{
		"From: SilkStrand <noreply@silkstrand.io>\r\n",
		"To: u@example.com\r\n",
		"Subject: Hello\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: multipart/alternative; boundary=\"_=_silkstrand_alt_boundary_=_\"\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"Content-Type: text/html; charset=UTF-8\r\n",
		"plain body\r\nline2", // LF normalized to CRLF
		"<p>html body</p>",
		"--_=_silkstrand_alt_boundary_=_--\r\n", // closing boundary
	}
	for _, m := range must {
		if !strings.Contains(msg, m) {
			t.Errorf("MIME message missing %q\n--- got ---\n%s", m, msg)
		}
	}
	if strings.Contains(msg, "\n") && !strings.Contains(msg, "\r\n") {
		t.Error("message must use CRLF line endings")
	}
}
