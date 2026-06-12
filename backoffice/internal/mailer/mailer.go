// Package mailer sends transactional email. Implementations: SMTP relay (prod)
// and Noop (local dev — logs the email contents).
package mailer

// Mailer is the abstract interface used by handlers. Multiple provider
// implementations can satisfy it (SMTP relay -> SES today).
type Mailer interface {
	// SendInvite sends an organization invitation email containing a link
	// the recipient clicks to accept.
	SendInvite(to, inviteURL, tenantName string) error

	// SendPasswordReset sends an email containing a password-reset link.
	SendPasswordReset(to, resetURL string) error
}
