package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ErrTopicHasNoMagnet = errors.New("rutracker topic has no magnet link")
	topicIDPattern      = regexp.MustCompile(`^[0-9]+$`)
	infoHashOnlyPattern = regexp.MustCompile(`(?i)^[0-9a-f]{40}$`)
	magnetLinkPattern   = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']*magnet:\?[^"']+)["']`)
	infoHashPattern     = regexp.MustCompile(`(?i)(?:^|[?&])xt=urn:btih:([0-9a-f]{40})(?:&|$)`)
)

type ResolvedTopic struct {
	TopicID  string
	InfoHash string
	Magnet   string
}

type TopicResolver interface {
	Resolve(context.Context, string) (*ResolvedTopic, error)
}

// RutrackerForumResolver resolves an immutable RuTracker topic id by loading
// its exact forum page through a generic FlareSolverr HTTP endpoint. It knows
// nothing about how either service is deployed.
type RutrackerForumResolver struct {
	forumBase       string
	flaresolverrURL string
	http            *http.Client
}

func NewRutrackerForumResolver(forumBase, flaresolverrURL string) *RutrackerForumResolver {
	return &RutrackerForumResolver{
		forumBase:       strings.TrimRight(forumBase, "/"),
		flaresolverrURL: strings.TrimRight(flaresolverrURL, "/"),
		http:            &http.Client{Timeout: 90 * time.Second},
	}
}

func (r *RutrackerForumResolver) Resolve(ctx context.Context, topicID string) (*ResolvedTopic, error) {
	if !topicIDPattern.MatchString(topicID) {
		return nil, fmt.Errorf("invalid rutracker topic id %q", topicID)
	}
	topicURL := r.forumBase + "/viewtopic.php?t=" + url.QueryEscape(topicID)
	payload, err := json.Marshal(struct {
		Command    string `json:"cmd"`
		URL        string `json:"url"`
		MaxTimeout int    `json:"maxTimeout"`
	}{
		Command:    "request.get",
		URL:        topicURL,
		MaxTimeout: 60_000,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.flaresolverrURL+"/v1", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr request for topic %s: %w", topicID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read flaresolverr response for topic %s: %w", topicID, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("flaresolverr request for topic %s: status=%d", topicID, resp.StatusCode)
	}
	var result struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status   int    `json:"status"`
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse flaresolverr response for topic %s: %w", topicID, err)
	}
	if result.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr failed for topic %s: %s", topicID, result.Message)
	}
	if result.Solution.Status >= http.StatusBadRequest {
		return nil, fmt.Errorf("rutracker topic %s: status=%d", topicID, result.Solution.Status)
	}

	page := html.UnescapeString(result.Solution.Response)
	match := magnetLinkPattern.FindStringSubmatch(page)
	if len(match) != 2 {
		if strings.Contains(strings.ToLower(page), "just a moment") || strings.Contains(strings.ToLower(page), "cf-chl-") {
			return nil, fmt.Errorf("cloudflare challenge was not solved for topic %s", topicID)
		}
		return nil, fmt.Errorf("topic %s: %w", topicID, ErrTopicHasNoMagnet)
	}
	magnet := match[1]
	parsedMagnet, err := url.Parse(magnet)
	if err != nil {
		return nil, fmt.Errorf("parse magnet for topic %s: %w", topicID, err)
	}
	hashMatch := infoHashPattern.FindStringSubmatch("?" + parsedMagnet.RawQuery)
	if len(hashMatch) != 2 {
		return nil, fmt.Errorf("topic %s returned magnet without a v1 info hash", topicID)
	}
	return &ResolvedTopic{
		TopicID:  topicID,
		InfoHash: strings.ToLower(hashMatch[1]),
		Magnet:   magnet,
	}, nil
}
