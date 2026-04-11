package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type ProwlarrClient struct {
	base   string
	apiKey string
	http   *http.Client
}

type SearchResult struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	InfoURL     string `json:"infoUrl"`
	DownloadURL string `json:"downloadUrl"`
	IndexerID   int    `json:"indexerId"`
	Indexer     string `json:"indexer"`
	Seeders     int    `json:"seeders"`
	Size        int64  `json:"size"`
}

func NewProwlarrClient(base, apiKey string) *ProwlarrClient {
	return &ProwlarrClient{
		base:   strings.TrimRight(base, "/"),
		apiKey: apiKey,
		http:   &http.Client{},
	}
}

func (p *ProwlarrClient) do(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", p.apiKey)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: status=%d body=%s", method, path, resp.StatusCode, string(data))
	}
	return data, nil
}

func (p *ProwlarrClient) Search(ctx context.Context, query string, indexerIDs []int, limit int) ([]SearchResult, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("type", "search")
	for _, id := range indexerIDs {
		q.Add("indexerIds", fmt.Sprintf("%d", id))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	data, err := p.do(ctx, "GET", "/api/v1/search?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return out, nil
}

func (p *ProwlarrClient) Download(ctx context.Context, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(downloadURL, "apikey=") {
		req.Header.Set("X-Api-Key", p.apiKey)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download %s: status=%d", downloadURL, resp.StatusCode)
	}
	if len(data) < 10 || data[0] != 'd' {
		return nil, fmt.Errorf("response does not look like a .torrent file (len=%d)", len(data))
	}
	return data, nil
}

// IndexerIDForTracker resolves a Prowlarr indexer id by looking at the host
// of the topic URL and matching it against configured indexer URLs.
func (p *ProwlarrClient) IndexerIDForTracker(ctx context.Context, topicURL string) (int, error) {
	data, err := p.do(ctx, "GET", "/api/v1/indexer")
	if err != nil {
		return 0, err
	}
	var indexers []struct {
		ID          int      `json:"id"`
		Name        string   `json:"name"`
		IndexerURLs []string `json:"indexerUrls"`
		Enable      bool     `json:"enable"`
	}
	if err := json.Unmarshal(data, &indexers); err != nil {
		return 0, err
	}
	parsed, err := url.Parse(topicURL)
	if err != nil {
		return 0, err
	}
	host := strings.ToLower(parsed.Host)
	for _, idx := range indexers {
		if !idx.Enable {
			continue
		}
		for _, u := range idx.IndexerURLs {
			pu, err := url.Parse(u)
			if err != nil {
				continue
			}
			if strings.EqualFold(pu.Host, host) {
				return idx.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("no enabled Prowlarr indexer matches host %q", host)
}

// FindByTopicID searches the indexer that serves topicURL using the provided
// query, then returns the result whose infoUrl points at the exact same
// topic id. This is used to get a valid (encrypted) Prowlarr downloadUrl for
// a specific rutracker topic.
func (p *ProwlarrClient) FindByTopicID(ctx context.Context, topicURL, query string) (*SearchResult, error) {
	indexerID, err := p.IndexerIDForTracker(ctx, topicURL)
	if err != nil {
		return nil, err
	}
	topicID := extractTopicID(topicURL)
	if topicID == "" {
		return nil, fmt.Errorf("cannot extract topic id from %q", topicURL)
	}

	results, err := p.Search(ctx, query, []int{indexerID}, 100)
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", query, err)
	}
	for i := range results {
		if extractTopicID(results[i].InfoURL) == topicID {
			return &results[i], nil
		}
	}
	return nil, fmt.Errorf("topic id %s not found in %d Prowlarr results for query %q", topicID, len(results), query)
}

func extractTopicID(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	if id := parsed.Query().Get("t"); id != "" {
		return id
	}
	if id := parsed.Query().Get("id"); id != "" {
		return id
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		last = strings.TrimSuffix(last, ".html")
		if last != "" {
			return last
		}
	}
	return ""
}
