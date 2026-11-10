// Package sentry reads an issue's latest event from Sentry's API, for the
// details an issue webhook does not carry.
package sentry

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rahacloud/sentrygram/internal/relay"
)

// Client reads events with an internal integration's token (scope event:read).
type Client struct {
	BaseURL string
	Token   string
	// Org is the organization slug; when empty it is read from the issue's web URL.
	Org  string
	HTTP *http.Client
}

// New returns a client for the Sentry at baseURL.
func New(baseURL, token, org string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Org: org, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type rawFrame struct {
	Filename string  `json:"filename"`
	AbsPath  string  `json:"absPath"`
	Module   string  `json:"module"`
	Function string  `json:"function"`
	LineNo   int     `json:"lineNo"`
	InApp    bool    `json:"inApp"`
	Context  [][]any `json:"context"`
}

type entry struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type exceptionData struct {
	Values []struct {
		Mechanism *struct {
			Handled *bool `json:"handled"`
		} `json:"mechanism"`
		Stacktrace *struct {
			Frames []rawFrame `json:"frames"`
		} `json:"stacktrace"`
	} `json:"values"`
}

type requestData struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

type rawEvent struct {
	Tags    []tag   `json:"tags"`
	Entries []entry `json:"entries"`
	Release *struct {
		Version      string `json:"version"`
		ShortVersion string `json:"shortVersion"`
	} `json:"release"`
	Contexts map[string]struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"contexts"`
}

// orgFromURL reads the organization slug out of an issue's web URL
// (https://sentry.example/organizations/<org>/issues/<id>/).
func orgFromURL(webURL string) string {
	u, err := url.Parse(webURL)
	if err != nil {
		return ""
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if p == "organizations" && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return ""
}

// LatestEvent reads the newest event of issue.
func (c *Client) LatestEvent(ctx context.Context, issue relay.Issue) (*relay.Event, error) {
	org := c.Org
	if org == "" {
		org = orgFromURL(issue.WebURL)
	}

	if org == "" || issue.ID == "" {
		return nil, errors.New("no organization or issue id to read the event with")
	}

	endpoint := c.BaseURL + "/api/0/organizations/" + url.PathEscape(org) + "/issues/" + url.PathEscape(issue.ID) + "/events/latest/"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Sentry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sentry answered %d", resp.StatusCode)
	}

	var raw rawEvent
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding the event: %w", err)
	}

	return convert(raw), nil
}

func convert(raw rawEvent) *relay.Event {
	tags := make(map[string]string, len(raw.Tags))
	for _, t := range raw.Tags {
		tags[t.Key] = t.Value
	}

	ev := &relay.Event{
		Environment: tags["environment"],
		Transaction: tags["transaction"],
		ServerName:  tags["server_name"],
		Browser:     tags["browser"],
		OS:          tags["os"],
		Runtime:     tags["runtime"],
		Release:     tags["release"],
	}

	if raw.Release != nil {
		ev.Release = cmp.Or(raw.Release.ShortVersion, raw.Release.Version, ev.Release)
	}

	for _, e := range raw.Entries {
		switch e.Type {
		case "exception":
			var data exceptionData
			if json.Unmarshal(e.Data, &data) == nil {
				exception(ev, data)
			}
		case "request":
			var data requestData
			if json.Unmarshal(e.Data, &data) == nil {
				ev.Method, ev.URL = data.Method, data.URL
			}
		}
	}

	return ev
}

// exception takes the handled flag and the frame from the last exception
// value, the one that was raised; Sentry lists frames outermost first.
func exception(ev *relay.Event, data exceptionData) {
	if len(data.Values) == 0 {
		return
	}

	last := data.Values[len(data.Values)-1]
	if last.Mechanism != nil {
		ev.Handled = last.Mechanism.Handled
	}

	if last.Stacktrace == nil || len(last.Stacktrace.Frames) == 0 {
		return
	}

	frames := last.Stacktrace.Frames
	pick := frames[len(frames)-1]

	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].InApp {
			pick = frames[i]

			break
		}
	}

	ev.Frame = &relay.Frame{
		File:     cmp.Or(pick.Filename, pick.AbsPath, pick.Module),
		Line:     pick.LineNo,
		Function: pick.Function,
		Code:     codeAt(pick),
	}
}

// codeAt is the source line the frame stopped on, from its context lines.
func codeAt(f rawFrame) string {
	for _, pair := range f.Context {
		if len(pair) != 2 {
			continue
		}

		n, ok := pair[0].(float64)
		code, isString := pair[1].(string)

		if ok && isString && int(n) == f.LineNo {
			return code
		}
	}

	return ""
}
