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

// GetTopics returns a map of topic_id -> TopicInfo for the given ids.
// A nil entry means rutracker reported the topic does not exist (deleted
// or never existed). Missing keys mean the same — the API includes every
// requested id in its response. Requests are split into chunks of
// rutrackerBatchSize so a single URL stays well under server limits.
func (r *RutrackerAPI) GetTopics(ctx context.Context, topicIDs []string) (map[string]*TopicInfo, error) {
	out := make(map[string]*TopicInfo, len(topicIDs))
	// rutracker API caps a single request at 50 ids; larger batches return
	// {"error": {"code": 1, "text": "Param [val] is over the limit of 50"}}.
	const chunkSize = 50
	for i := 0; i < len(topicIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(topicIDs) {
			end = len(topicIDs)
		}
		if err := r.fetchBatch(ctx, topicIDs[i:end], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *RutrackerAPI) fetchBatch(ctx context.Context, ids []string, out map[string]*TopicInfo) error {
	val := strings.Join(ids, ",")
	u := fmt.Sprintf("%s/get_tor_topic_data?by=topic_id&val=%s", r.base, url.QueryEscape(val))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("rutracker api %d: %s", resp.StatusCode, string(data))
	}
	var wrapper struct {
		Result map[string]json.RawMessage `json:"result"`
		Error  *struct {
			Code int    `json:"code"`
			Text string `json:"text"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return fmt.Errorf("parse rutracker api: %w", err)
	}
	if wrapper.Error != nil {
		return fmt.Errorf("rutracker api error %d: %s", wrapper.Error.Code, wrapper.Error.Text)
	}
	for _, id := range ids {
		raw, ok := wrapper.Result[id]
		if !ok || string(raw) == "null" {
			out[id] = nil
			continue
		}
		var info TopicInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			return fmt.Errorf("parse topic info %s: %w", id, err)
		}
		out[id] = &info
	}
	return nil
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
