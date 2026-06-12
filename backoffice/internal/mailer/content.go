package mailer

import "fmt"

// Shared email content builders so every Mailer implementation (SMTP
// ...) sends byte-identical messages. Each returns subject, plain-text, and
// HTML bodies.

func inviteEmail(inviteURL, tenantName string) (subject, text, html string) {
	subject = fmt.Sprintf("You're invited to %s on SilkStrand", tenantName)
	text = fmt.Sprintf(
		"You've been invited to join %s on SilkStrand.\n\nAccept your invitation:\n%s\n\nThis link expires in 7 days.",
		tenantName, inviteURL)
	html = fmt.Sprintf(`<!doctype html>
<html><body style="font-family:sans-serif;max-width:560px;margin:40px auto;padding:0 20px;color:#111">
<h2 style="margin:0 0 16px">You're invited to %s</h2>
<p>You've been invited to join <strong>%s</strong> on SilkStrand.</p>
<p style="margin:24px 0">
  <a href="%s" style="display:inline-block;background:#0f766e;color:#fff;padding:10px 20px;border-radius:6px;text-decoration:none">Accept invitation</a>
</p>
<p style="font-size:13px;color:#555">Or paste this link into your browser:<br><a href="%s">%s</a></p>
<p style="font-size:13px;color:#555">This link expires in 7 days.</p>
</body></html>`, tenantName, tenantName, inviteURL, inviteURL, inviteURL)
	return
}

func passwordResetEmail(resetURL string) (subject, text, html string) {
	subject = "Reset your SilkStrand password"
	text = fmt.Sprintf(
		"We received a request to reset your SilkStrand password.\n\nReset it here:\n%s\n\nThis link expires in 1 hour. If you didn't request a reset, ignore this email.",
		resetURL)
	html = fmt.Sprintf(`<!doctype html>
<html><body style="font-family:sans-serif;max-width:560px;margin:40px auto;padding:0 20px;color:#111">
<h2 style="margin:0 0 16px">Reset your password</h2>
<p>We received a request to reset your SilkStrand password.</p>
<p style="margin:24px 0">
  <a href="%s" style="display:inline-block;background:#0f766e;color:#fff;padding:10px 20px;border-radius:6px;text-decoration:none">Reset password</a>
</p>
<p style="font-size:13px;color:#555">Or paste this link into your browser:<br><a href="%s">%s</a></p>
<p style="font-size:13px;color:#555">This link expires in 1 hour. If you didn't request a reset, ignore this email.</p>
</body></html>`, resetURL, resetURL, resetURL)
	return
}
