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

// TopicInfo is a subset of the rutracker public API response.
type TopicInfo struct {
	InfoHash   string  `json:"info_hash"`
	ForumID    int     `json:"forum_id"`
	PosterID   int     `json:"poster_id"`
	Size       float64 `json:"size"`
	RegTime    int64   `json:"reg_time"`
	TorStatus  int     `json:"tor_status"`
	Seeders    int     `json:"seeders"`
	TopicTitle string  `json:"topic_title"`
	DLCount    int     `json:"dl_count"`
}

// RutrackerAPI is a minimal client for api.rutracker.cc (public, no auth).
type RutrackerAPI struct {
	base string
	http *http.Client
}

func NewRutrackerAPI() *RutrackerAPI {
	return &RutrackerAPI{
		base: "https://api.rutracker.cc/v1",
		http: &http.Client{},
	}
}

// GetTopic returns TopicInfo for the given topic id, or (nil, nil) if the
// topic no longer exists on rutracker.
func (r *RutrackerAPI) GetTopic(ctx context.Context, topicID string) (*TopicInfo, error) {
	u := fmt.Sprintf("%s/get_tor_topic_data?by=topic_id&val=%s", r.base, url.QueryEscape(topicID))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("rutracker api %d: %s", resp.StatusCode, string(data))
	}
	var wrapper struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parse rutracker api: %w", err)
	}
	raw, ok := wrapper.Result[topicID]
	if !ok {
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var info TopicInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("parse topic info: %w", err)
	}
	return &info, nil
}

// IsRutrackerTopic reports whether the URL looks like a rutracker topic URL.
func IsRutrackerTopic(topicURL string) bool {
	u, err := url.Parse(topicURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.HasSuffix(host, "rutracker.org") ||
		strings.HasSuffix(host, "rutracker.net") ||
		strings.HasSuffix(host, "rutracker.nl") ||
		strings.HasSuffix(host, "rutracker.cc")
}
