package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

type QbitClient struct {
	base   string
	http   *http.Client
	user   string
	pass   string
	logged bool
}

type Torrent struct {
	Hash     string `json:"hash"`
	Name     string `json:"name"`
	SavePath string `json:"save_path"`
	Tracker  string `json:"tracker"`
	Category string `json:"category"`
	Tags     string `json:"tags"`
}

type TorrentProperties struct {
	Comment string `json:"comment"`
}

type Tracker struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

func NewQbitClient(base, user, pass string) (*QbitClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &QbitClient{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Jar: jar},
		user: user,
		pass: pass,
	}, nil
}

func (c *QbitClient) Login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.user)
	form.Set("password", c.pass)
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qbit login: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Ok") {
		return fmt.Errorf("qbit login failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	c.logged = true
	return nil
}

func (c *QbitClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
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

func (c *QbitClient) AddTorrent(ctx context.Context, torrentBytes []byte, name string, savePath string, category string, tags string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("torrents", name+".torrent")
	if err != nil {
		return err
	}
	if _, err := fw.Write(torrentBytes); err != nil {
		return err
	}

	fields := map[string]string{
		"savepath":      savePath,
		"skip_checking": "false",
		"autoTMM":       "false",
	}
	if category != "" {
		fields["category"] = category
	}
	if tags != "" {
		fields["tags"] = tags
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}

	_, err = c.do(ctx, "POST", "/api/v2/torrents/add", &buf, mw.FormDataContentType())
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
