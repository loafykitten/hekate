package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxResults   = 8
	maxQueryLen  = 400
	maxBraveBody = 2 << 20
)

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

var errNoKey = errors.New("web search is not configured")

// Search runs a Brave web search for up to count results (1..8, default 5).
func (s *Service) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if !s.SearchConfigured() {
		return nil, errNoKey
	}
	query = strings.Join(strings.Fields(query), " ")
	switch {
	case query == "":
		return nil, errors.New("query is required")
	case len([]rune(query)) > maxQueryLen:
		return nil, fmt.Errorf("query is too long (at most %d characters)", maxQueryLen)
	}
	if count <= 0 {
		count = 5
	}
	count = min(count, maxResults)

	q := url.Values{}
	q.Set("q", query)
	q.Set("count", strconv.Itoa(count))
	q.Set("result_filter", "web")
	q.Set("safesearch", "moderate")
	q.Set("text_decorations", "false")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.braveURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", s.key.Reveal())

	resp, err := s.search.Do(req)
	if err != nil {
		// The URL holds the query, not the key; still report only the cause.
		return nil, fmt.Errorf("search request failed: %w", unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("the search service rejected Hekate's API key")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("the search service is rate limiting Hekate; try again in a moment")
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("the search service answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBraveBody))
	if err != nil {
		return nil, fmt.Errorf("search response: %w", unwrapURLError(err))
	}
	return parseBrave(body, count)
}

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Profile     struct {
				Name string `json:"name"`
			} `json:"profile"`
			MetaURL struct {
				Hostname string `json:"hostname"`
			} `json:"meta_url"`
		} `json:"results"`
	} `json:"web"`
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// plain strips the markup Brave sometimes leaves in titles and descriptions.
func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(s, ""))), " ")
}

func parseBrave(body []byte, limit int) ([]Result, error) {
	var r braveResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("could not read the search response: %w", err)
	}
	out := make([]Result, 0, len(r.Web.Results))
	for _, h := range r.Web.Results {
		u, err := url.Parse(h.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue // never hand the model or the app a non-web link
		}
		source := strings.TrimPrefix(h.MetaURL.Hostname, "www.")
		if source == "" {
			source = strings.TrimPrefix(u.Hostname(), "www.")
		}
		out = append(out, Result{
			Title:   plain(h.Title),
			URL:     h.URL,
			Snippet: plain(h.Description),
			Source:  source,
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
