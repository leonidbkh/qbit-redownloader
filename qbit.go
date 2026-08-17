package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type QbitClient struct {
	base   string
	http   *http.Client
	apiKey string
}

type Torrent struct {
	Hash      string `json:"hash"`
	Name      string `json:"name"`
	SavePath  string `json:"save_path"`
	Tracker   string `json:"tracker"`
	Category  string `json:"category"`
	Tags      string `json:"tags"`
	State     string `json:"state"`
	TotalSize int64  `json:"total_size"`
}

type TorrentProperties struct {
	Comment string `json:"comment"`
}

type Tracker struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

func NewQbitClient(base, apiKey string) (*QbitClient, error) {
	return &QbitClient{
		base:   strings.TrimRight(base, "/"),
		http:   &http.Client{},
		apiKey: apiKey,
	}, nil
}

func (c *QbitClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Referer", c.base)
	resp, err := c.http.Do(req)
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

func (c *QbitClient) ListTorrents(ctx context.Context) ([]Torrent, error) {
	data, err := c.do(ctx, "GET", "/api/v2/torrents/info", nil, "")
	if err != nil {
		return nil, err
	}
	var out []Torrent
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse torrents: %w", err)
	}
	return out, nil
}

func (c *QbitClient) TorrentByHash(ctx context.Context, hash string) (*Torrent, error) {
	data, err := c.do(ctx, "GET", "/api/v2/torrents/info?hashes="+url.QueryEscape(strings.ToLower(hash)), nil, "")
	if err != nil {
		return nil, err
	}
	var torrents []Torrent
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, fmt.Errorf("parse torrent lookup: %w", err)
	}
	for i := range torrents {
		if strings.EqualFold(torrents[i].Hash, hash) {
			return &torrents[i], nil
		}
	}
	return nil, nil
}

func (c *QbitClient) Properties(ctx context.Context, hash string) (*TorrentProperties, error) {
	data, err := c.do(ctx, "GET", "/api/v2/torrents/properties?hash="+hash, nil, "")
	if err != nil {
		return nil, err
	}
	var out TorrentProperties
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse properties: %w", err)
	}
	return &out, nil
}

func (c *QbitClient) Trackers(ctx context.Context, hash string) ([]Tracker, error) {
	data, err := c.do(ctx, "GET", "/api/v2/torrents/trackers?hash="+hash, nil, "")
	if err != nil {
		return nil, err
	}
	var out []Tracker
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse trackers: %w", err)
	}
	return out, nil
}

func (c *QbitClient) AddMagnet(ctx context.Context, magnet, savePath, category, tags string) error {
	form := url.Values{}
	form.Set("urls", magnet)
	form.Set("savepath", savePath)
	form.Set("skip_checking", "false")
	form.Set("autoTMM", "false")
	form.Set("stopped", "false")
	if category != "" {
		form.Set("category", category)
	}
	if tags != "" {
		form.Set("tags", tags)
	}
	_, err := c.do(ctx, "POST", "/api/v2/torrents/add", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return err
}

func (c *QbitClient) WaitForTorrentReady(ctx context.Context, hash string, timeout time.Duration) (*Torrent, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		torrent, err := c.TorrentByHash(ctx, hash)
		if err != nil {
			return nil, err
		}
		if torrent != nil && torrent.TotalSize > 0 && torrent.State != "metaDL" {
			return torrent, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("torrent %s did not become ready within %s", hash, timeout)
		case <-ticker.C:
		}
	}
}

func (c *QbitClient) Stop(ctx context.Context, hash string) error {
	form := url.Values{}
	form.Set("hashes", hash)
	_, err := c.do(ctx, "POST", "/api/v2/torrents/stop", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return err
}

func (c *QbitClient) SetComment(ctx context.Context, hash, comment string) error {
	form := url.Values{}
	form.Set("hashes", hash)
	form.Set("comment", comment)
	_, err := c.do(ctx, "POST", "/api/v2/torrents/setComment", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return err
}

func (c *QbitClient) AddTags(ctx context.Context, hash, tags string) error {
	form := url.Values{}
	form.Set("hashes", hash)
	form.Set("tags", tags)
	_, err := c.do(ctx, "POST", "/api/v2/torrents/addTags", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return err
}

func (c *QbitClient) RemoveTags(ctx context.Context, hash, tags string) error {
	form := url.Values{}
	form.Set("hashes", hash)
	form.Set("tags", tags)
	_, err := c.do(ctx, "POST", "/api/v2/torrents/removeTags", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return err
}

func (c *QbitClient) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	form := url.Values{}
	form.Set("hashes", hash)
	if deleteFiles {
		form.Set("deleteFiles", "true")
	} else {
		form.Set("deleteFiles", "false")
	}
	_, err := c.do(ctx, "POST", "/api/v2/torrents/delete", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return err
}
