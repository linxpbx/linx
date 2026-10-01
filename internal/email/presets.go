package email

// Preset is one "Who sends it?" choice on the Email card
// (docs/ui/SCREENS_PHASE1F.md §5.1): its server, and what the page says
// about signing in. The web page reads the same list
// (web/src/lib/email-presets.json, kept current by TestPresetsFixture).
type Preset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Security string `json:"security,omitempty"`
	// Password is what the password field is called ("App password").
	Password string `json:"password"`
	// UsernameIsAddress: the account's name is the address it sends from.
	// Otherwise the page asks for the user name (a sending service's key
	// name or SMTP user).
	UsernameIsAddress bool `json:"username_is_address"`
	// Username is what the user name field is called, when it's asked.
	Username string `json:"username,omitempty"`
	// Where is how to get the password, in plain words.
	Where string `json:"where"`
	// Link is the provider's own page for it.
	Link string `json:"link,omitempty"`
	// Warning is shown with the choice.
	Warning string `json:"warning,omitempty"`
}

// PresetOther asks for the server and port too.
const PresetOther = "other"

// Presets, in the card's order.
var Presets = []Preset{
	{ID: "google", Name: "Gmail or Google Workspace", Host: "smtp.gmail.com", Port: 465, Security: SecurityTLS,
		Password: "App password", UsernameIsAddress: true,
		Where: "Turn on 2-Step Verification for the account, then make an app password at myaccount.google.com → Security → App passwords (name it Linx). Paste the 16 letters, without spaces.",
		Link:  "https://myaccount.google.com/apppasswords"},
	{ID: "microsoft", Name: "Microsoft 365", Host: "smtp.office365.com", Port: 587, Security: SecuritySTARTTLS,
		Password: "Password or app password", UsernameIsAddress: true,
		Where:   "Your admin has to allow \"Authenticated SMTP\" for this mailbox (Microsoft 365 admin center → Users → the mailbox → Mail → Email apps). With 2-step sign-in, make an app password at mysignins.microsoft.com → Security info.",
		Link:    "https://mysignins.microsoft.com/security-info",
		Warning: "Microsoft is turning off password sign-in for sending mail. It may stop working; a sending service (Postmark, Brevo, Amazon SES) is safer."},
	{ID: "icloud", Name: "iCloud Mail", Host: "smtp.mail.me.com", Port: 587, Security: SecuritySTARTTLS,
		Password: "App-specific password", UsernameIsAddress: true,
		Where: "Make an app-specific password at account.apple.com → Sign-In and Security → App-Specific Passwords (name it Linx). Send from your @icloud.com address or a custom domain set up in iCloud.",
		Link:  "https://account.apple.com"},
	{ID: "fastmail", Name: "Fastmail", Host: "smtp.fastmail.com", Port: 465, Security: SecurityTLS,
		Password: "App password", UsernameIsAddress: true,
		Where: "Make an app password at Settings → Privacy & Security → Integrations → App passwords, with access to SMTP only.",
		Link:  "https://app.fastmail.com/settings/security/integrations"},
	{ID: "ses", Name: "Amazon SES", Port: 465, Security: SecurityTLS,
		Password: "SMTP password", Username: "SMTP user name",
		Where: "In the SES console of your region: verify the address or domain you send from, then SMTP settings → Create SMTP credentials. Give the SMTP endpoint shown there as the server (e.g. email-smtp.eu-west-1.amazonaws.com)."},
	{ID: "postmark", Name: "Postmark", Host: "smtp.postmarkapp.com", Port: 587, Security: SecuritySTARTTLS,
		Password: "Server API token", Username: "Server API token",
		Where: "In Postmark, verify your sender address or domain, then open your server → API Tokens. The same token is the user name and the password.",
		Link:  "https://account.postmarkapp.com/servers"},
	{ID: "brevo", Name: "Brevo", Host: "smtp-relay.brevo.com", Port: 587, Security: SecuritySTARTTLS,
		Password: "SMTP key", Username: "SMTP login",
		Where: "In Brevo, add and verify your sender, then Settings → SMTP & API → SMTP: the login is shown there, and Generate a new SMTP key gives the password.",
		Link:  "https://app.brevo.com/settings/keys/smtp"},
	{ID: "mailgun", Name: "Mailgun", Host: "smtp.mailgun.org", Port: 465, Security: SecurityTLS,
		Password: "SMTP password", Username: "SMTP user name",
		Where: "In Mailgun, open your sending domain → Domain settings → SMTP credentials and add one. For the EU region, the server is smtp.eu.mailgun.org."},
	{ID: PresetOther, Name: "Something else", Password: "Password", Username: "User name",
		Where: "Your mail provider's help pages list its SMTP server, port and encryption. Linx only sends encrypted: TLS from the start (usually port 465) or STARTTLS (usually 587)."},
}

// PresetByID returns the preset with id, if there is one.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
