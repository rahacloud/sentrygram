// Package relay turns Sentry integration webhooks into Telegram messages.
package relay

import (
	"html"
	"strings"
)

// Issue is the part of Sentry's issue webhook payload (data.issue) a message uses.
type Issue struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"shortId"`
	Title    string   `json:"title"`
	Culprit  string   `json:"culprit"`
	Level    string   `json:"level"`
	WebURL   string   `json:"web_url"`
	Project  Project  `json:"project"`
	Metadata Metadata `json:"metadata"`
}

// Project names the Sentry project an issue belongs to.
type Project struct {
	Slug string `json:"slug"`
}

// Metadata is the exception an issue was grouped on.
type Metadata struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Action is what happened to an issue, as Sentry names it.
type Action string

// The actions that are posted; every other one is dropped.
const (
	Created    Action = "created"
	Unresolved Action = "unresolved"
)

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}

	return string(r[:n-1]) + "…"
}

// Message writes the Telegram HTML for an issue. sentryURL builds the link
// when the payload carries none.
func Message(issue Issue, action Action, sentryURL string) string {
	level := strings.ToUpper(issue.Level)
	if level == "" {
		level = "ERROR"
	}

	project := issue.Project.Slug
	if project == "" {
		project = "?"
	}

	icon, suffix := "🚨", ""
	if action == Unresolved {
		icon, suffix = "🔁", " · <i>back again</i>"
	}

	title := issue.Title
	if issue.Metadata.Type != "" {
		title = issue.Metadata.Type + ": " + issue.Metadata.Value
	}

	var b strings.Builder

	b.WriteString(icon + " <b>" + html.EscapeString(level) + "</b> · <b>" + html.EscapeString(project) + "</b>" + suffix + "\n")
	b.WriteString(html.EscapeString(clip(title, 500)))

	if issue.Culprit != "" {
		b.WriteString("\n📍 <code>" + html.EscapeString(clip(issue.Culprit, 200)) + "</code>")
	}

	url := issue.WebURL
	if url == "" {
		url = strings.TrimRight(sentryURL, "/") + "/issues/" + issue.ID + "/"
	}

	label := issue.ShortID
	if label == "" {
		label = "Open in Sentry"
	}

	b.WriteString("\n<a href=\"" + html.EscapeString(url) + "\">" + html.EscapeString(label) + " →</a>")

	return b.String()
}
