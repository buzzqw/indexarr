package i18n

// Notification settings and grab-success strings are added here instead of in
// locales/*.json: the catalogs are owned by the translation update that lands
// with this block. The fallback is additive and never replaces a key that a
// catalog already has, so the real translation wins as soon as it arrives.
var notificationFallbacks = map[string]string{
	"settings.channelsExtendedHelp":  "Besides the webhook and Telegram, indexarr can post to Apprise, Discord, Slack, Gotify, ntfy, Pushover, email and a custom script. Configure only the channels you use.",
	"settings.appriseTitle":          "Apprise",
	"settings.appriseHelp":           "Send to an Apprise API server, which forwards the message to the services you configured there.",
	"settings.appriseURL":            "Apprise API URL",
	"settings.appriseURLHelp":        "Address of the Apprise API server, for example http://192.168.1.10:8000. indexarr posts to /notify.",
	"settings.appriseKey":            "Apprise key",
	"settings.appriseKeyHelp":        "Optional key of one Apprise configuration; leave it empty to use the default one. It is encrypted in the vault.",
	"settings.discordTitle":          "Discord",
	"settings.discordHelp":           "Post the message in a Discord channel through an incoming webhook.",
	"settings.discordURL":            "Discord webhook URL",
	"settings.discordURLHelp":        "The incoming webhook URL from Discord, under Channel settings → Integrations. It is encrypted in the vault.",
	"settings.slackTitle":            "Slack",
	"settings.slackHelp":             "Post the message in a Slack channel through an incoming webhook.",
	"settings.slackURL":              "Slack webhook URL",
	"settings.slackURLHelp":          "The incoming webhook URL from Slack, under Apps → Incoming Webhooks. It is encrypted in the vault.",
	"settings.gotifyTitle":           "Gotify",
	"settings.gotifyHelp":            "Push the message to a Gotify server and its mobile app.",
	"settings.gotifyURL":             "Gotify server URL",
	"settings.gotifyURLHelp":         "Address of the Gotify server, for example https://gotify.example.com.",
	"settings.gotifyToken":           "Gotify app token",
	"settings.gotifyTokenHelp":       "Token of the Gotify application that receives the message. It is encrypted in the vault.",
	"settings.ntfyTitle":             "ntfy",
	"settings.ntfyHelp":              "Publish the message on an ntfy topic, read with the ntfy app or a browser.",
	"settings.ntfyURL":               "ntfy server URL",
	"settings.ntfyURLHelp":           "Address of the ntfy server; use https://ntfy.sh for the public one.",
	"settings.ntfyTopic":             "ntfy topic",
	"settings.ntfyTopicHelp":         "Topic that receives the message. Choose a name that is hard to guess when the server is public.",
	"settings.ntfyToken":             "ntfy access token",
	"settings.ntfyTokenHelp":         "Optional token for a protected topic. It is encrypted in the vault.",
	"settings.pushoverTitle":         "Pushover",
	"settings.pushoverHelp":          "Send the message to the Pushover app on your phone.",
	"settings.pushoverToken":         "Pushover application token",
	"settings.pushoverTokenHelp":     "Token of your Pushover application. It is encrypted in the vault.",
	"settings.pushoverUser":          "Pushover user key",
	"settings.pushoverUserHelp":      "Your Pushover user or group key. It is encrypted in the vault.",
	"settings.smtpTitle":             "Email",
	"settings.smtpHelp":              "Send the message by email over SMTP.",
	"settings.smtpHost":              "SMTP server",
	"settings.smtpHostHelp":          "Host and port of the mail server, for example smtp.example.com:587.",
	"settings.smtpStartTLS":          "Use STARTTLS",
	"settings.smtpStartTLSHelp":      "Upgrade the connection to TLS before sending. Turn it on for port 587; port 465 needs a different client.",
	"settings.smtpUsername":          "SMTP username",
	"settings.smtpUsernameHelp":      "Leave it empty when the server needs no login.",
	"settings.smtpPassword":          "SMTP password",
	"settings.smtpPasswordHelp":      "Password of the SMTP user. It is encrypted in the vault.",
	"settings.smtpFrom":              "Sender address",
	"settings.smtpFromHelp":          "Address that appears in the From field, for example indexarr@example.com.",
	"settings.smtpTo":                "Recipients",
	"settings.smtpToHelp":            "One or more addresses separated by commas.",
	"settings.scriptTitle":           "Custom script",
	"settings.scriptHelp":            "Run an executable on the indexarr host for every event.",
	"settings.scriptPath":            "Script path",
	"settings.scriptPathHelp":        "Executable to run, for example /usr/local/bin/indexarr-notify.",
	"settings.scriptArgs":            "Script arguments",
	"settings.scriptArgsHelp":        "Optional arguments separated by spaces; quotes keep a value with spaces together. The event travels in INDEXARR_* variables: event type, instance, application URL, release title, source, size, categories, health message and level, test.",
	"settings.grabSuccess":           "A grab succeeded",
	"settings.grabSuccessHelp":       "Send a message when Sonarr, Radarr or another app grabs a release through indexarr.",
	"settings.notificationsFooter":   "Each channel is independent: configure the ones you use and leave the others empty. A failed delivery is written in the log and never stops a search or an update. The test button sends to every configured channel.",
	"settings.secretStoredHelp":      "The value is stored encrypted in the vault and never appears in the page. Leave it empty to keep it unchanged.",
	"settings.appriseURLPlaceholder": "https://apprise.example.com",
	"settings.gotifyURLPlaceholder":  "https://gotify.example.com",
	"settings.ntfyURLPlaceholder":    "https://ntfy.sh",
	"settings.smtpHostPlaceholder":   "smtp.example.com:587",
	"settings.smtpFromPlaceholder":   "indexarr@example.com",
	"settings.smtpToPlaceholder":     "you@example.com",
	"settings.scriptPathPlaceholder": "/usr/local/bin/indexarr-notify",
	"settings.error.channelURL":      "Enter a complete http:// or https:// address.",
	"settings.error.apprisePair":     "Set both the Apprise API URL and its key, or remove the key.",
	"settings.error.gotifyPair":      "Set both the Gotify server URL and the app token, or neither.",
	"settings.error.ntfyPair":        "Set both the ntfy server URL and the topic, or neither.",
	"settings.error.pushoverPair":    "Set both the Pushover application token and the user key, or neither.",
	"settings.error.smtpIncomplete":  "Set the SMTP server, sender and recipients, or leave all three empty.",
	"settings.error.smtpHost":        "Enter the SMTP server as host:port, for example smtp.example.com:587.",
	"settings.error.smtpCredentials": "Set both the SMTP username and password, or neither.",
	"settings.error.smtpAddress":     "Enter a valid email address.",
	"settings.error.scriptPath":      "Enter a script path, or leave the arguments empty too.",
	"notify.grabSuccess":             "Download started: %s",
	"notify.grabSuccessApp":          "Download started for %s (%s)",
	"notify.msg.grabSuccess":         "✅ indexarr grabbed “%s”.",
	"notify.msg.grabSuccessApp":      "✅ %s grabbed “%s”.",
}

func init() {
	for _, lang := range supported {
		catalog := catalogs[lang]
		for key, value := range notificationFallbacks {
			if _, ok := catalog[key]; !ok {
				catalog[key] = value
			}
		}
	}
}
