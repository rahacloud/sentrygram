// Package relay turns Sentry integration webhooks into Telegram messages.
package relay

import (
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Issue is the part of Sentry's issue webhook payload (data.issue) a message uses.
type Issue struct {
	ID          string   `json:"id"`
	ShortID     string   `json:"shortId"`
	Title       string   `json:"title"`
	Culprit     string   `json:"culprit"`
	Level       string   `json:"level"`
	Platform    string   `json:"platform"`
	WebURL      string   `json:"web_url"`
	Count       string   `json:"count"`
	UserCount   int      `json:"userCount"`
	FirstSeen   string   `json:"firstSeen"`
	IsUnhandled bool     `json:"isUnhandled"`
	Priority    string   `json:"priority"`
	Project     Project  `json:"project"`
	Metadata    Metadata `json:"metadata"`
}

// Project names the Sentry project an issue belongs to.
type Project struct {
	Slug string `json:"slug"`
}

// Metadata is the exception an issue was grouped on.
type Metadata struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Filename string `json:"filename"`
	Function string `json:"function"`
}

// Event is what a message shows of an issue's latest event, when the relay
// may read it from Sentry's API.
type Event struct {
	Environment string
	Release     string
	Handled     *bool
	// Frame is the innermost in-app frame of the exception, or the innermost frame.
	Frame       *Frame
	Method, URL string
	Transaction string
	ServerName  string
	Browser     string
	OS          string
	Runtime     string
}

// Frame is one stack frame and the line of code it stopped on.
type Frame struct {
	File     string
	Line     int
	Function string
	Code     string
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

func esc(s string) string { return html.EscapeString(s) }

// withoutQuery drops a URL's query and fragment, which can carry tokens.
func withoutQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	u.RawQuery, u.Fragment = "", ""

	return u.String()
}

func firstSeen(first string) string {
	t, err := time.Parse(time.RFC3339Nano, first)
	if err != nil {
		// Sentry's serializers also write "2006-01-02 15:04:05.999999+00:00".
		if t, err = time.Parse("2006-01-02 15:04:05.999999999-07:00", first); err != nil {
			return ""
		}
	}

	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// Message writes the Telegram HTML for an issue. event, when not nil, adds the
// latest event's details; sentryURL builds the link when the payload carries none.
func Message(issue Issue, action Action, event *Event, sentryURL string) string {
	var b strings.Builder

	b.WriteString(headline(issue, action, event) + "\n")

	title := issue.Title
	if issue.Metadata.Type != "" {
		title = issue.Metadata.Type + ": " + issue.Metadata.Value
	}

	b.WriteString("<b>" + esc(clip(title, 400)) + "</b>")

	writeWhere(&b, issue, event)

	if event != nil && event.URL != "" {
		b.WriteString("\n🌐 " + esc(strings.TrimSpace(event.Method+" "+clip(withoutQuery(event.URL), 200))))
	} else if event != nil && event.Transaction != "" {
		b.WriteString("\n🌐 " + esc(clip(event.Transaction, 200)))
	}

	if facts := facts(issue, event); len(facts) > 0 {
		b.WriteString("\n🏷 " + esc(strings.Join(facts, " · ")))
	}

	if counts := counts(issue); counts != "" {
		b.WriteString("\n📊 " + esc(counts))
	}

	link := issue.WebURL
	if link == "" {
		link = strings.TrimRight(sentryURL, "/") + "/issues/" + issue.ID + "/"
	}

	label := issue.ShortID
	if label == "" {
		label = "Open in Sentry"
	}

	b.WriteString("\n<a href=\"" + esc(link) + "\">" + esc(label) + " →</a>")

	return b.String()
}

func headline(issue Issue, action Action, event *Event) string {
	level := strings.ToUpper(issue.Level)
	if level == "" {
		level = "ERROR"
	}

	project := issue.Project.Slug
	if project == "" {
		project = "?"
	}

	icon := "🚨"
	if action == Unresolved {
		icon = "🔁"
	}

	line := icon + " <b>" + esc(level) + "</b> · <b>" + esc(project) + "</b>"
	if event != nil && event.Environment != "" {
		line += " · " + esc(event.Environment)
	}

	if action == Unresolved {
		line += " · <i>back again</i>"
	}

	return line
}

func writeWhere(b *strings.Builder, issue Issue, event *Event) {
	if event != nil && event.Frame != nil && event.Frame.File != "" {
		f := event.Frame

		where := f.File
		if f.Line > 0 {
			where += ":" + strconv.Itoa(f.Line)
		}

		if f.Function != "" {
			where += " in " + f.Function
		}

		b.WriteString("\n📍 <code>" + esc(clip(where, 200)) + "</code>")

		if code := strings.TrimSpace(f.Code); code != "" {
			b.WriteString("\n<pre>" + esc(clip(code, 300)) + "</pre>")
		}

		return
	}

	switch {
	case issue.Metadata.Filename != "" && issue.Metadata.Function != "":
		b.WriteString("\n📍 <code>" + esc(clip(issue.Metadata.Filename+" in "+issue.Metadata.Function, 200)) + "</code>")
	case issue.Culprit != "":
		b.WriteString("\n📍 <code>" + esc(clip(issue.Culprit, 200)) + "</code>")
	}
}

func facts(issue Issue, event *Event) []string {
	var out []string

	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}

	if event != nil {
		if event.Release != "" {
			add("release " + clip(event.Release, 40))
		}
	}

	add(issue.Platform)

	// The event's mechanism is the more precise answer; the issue's flag fills in
	// when there is no event or it does not say.
	switch {
	case event != nil && event.Handled != nil && *event.Handled:
		add("handled")
	case event != nil && event.Handled != nil, issue.IsUnhandled:
		add("🔥 unhandled")
	}

	if issue.Priority != "" {
		add(issue.Priority + " priority")
	}

	if event != nil {
		if event.ServerName != "" {
			add("server " + event.ServerName)
		}
		add(event.Runtime)
		add(event.Browser)
		add(event.OS)
	}

	return out
}

func counts(issue Issue) string {
	var parts []string

	if issue.Count != "" && issue.Count != "0" {
		parts = append(parts, plural(issue.Count, "event"))
	}

	if issue.UserCount > 0 {
		parts = append(parts, plural(strconv.Itoa(issue.UserCount), "user"))
	}

	if first := firstSeen(issue.FirstSeen); first != "" {
		parts = append(parts, "first seen "+first)
	}

	return strings.Join(parts, " · ")
}

func plural(n, noun string) string {
	if n == "1" {
		return n + " " + noun
	}

	return n + " " + noun + "s"
}
