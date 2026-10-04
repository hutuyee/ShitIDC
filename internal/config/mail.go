package config

// MailConfig holds process-level mail defaults. SMTP credentials themselves
// are stored encrypted in system_settings and managed from the admin console,
// never from config files.
type MailConfig struct {
	// LoginNotify sends a best-effort notification mail on successful login
	// from a new IP (delivered through the mail.send queue).
	LoginNotify bool
}

func loadMail() MailConfig {
	return MailConfig{
		LoginNotify: env("LOGIN_NOTIFY", "false") == "true",
	}
}
