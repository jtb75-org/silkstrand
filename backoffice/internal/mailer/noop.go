package mailer

import "log/slog"

// Noop mailer just logs the email details. Used for local dev where no
// SMTP relay is configured, so developers can see invite/reset URLs
// in the server logs.
type Noop struct{}

func (Noop) SendInvite(to, inviteURL, tenantName string) error {
	slog.Info("[mailer:noop] invite",
		"to", to, "tenant", tenantName, "url", inviteURL)
	return nil
}

func (Noop) SendPasswordReset(to, resetURL string) error {
	slog.Info("[mailer:noop] password reset",
		"to", to, "url", resetURL)
	return nil
}
