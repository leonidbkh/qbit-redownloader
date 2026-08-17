package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectAndPlanFallsBackToExactTopicWhenAPIIsDisabled(t *testing.T) {
	t.Parallel()

	const (
		topicID      = "6875876"
		oldHash      = "1111111111111111111111111111111111111111"
		expectedHash = "B99748278658EA9CB129C21F570C5EE3BAA99D13"
	)

	qbitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			fmt.Fprintf(w, `[{"hash":%q,"name":"release","save_path":"/downloads","category":"tv","tags":"tracked"}]`, oldHash)
		case "/api/v2/torrents/properties":
			fmt.Fprintf(w, `{"comment":"https://rutracker.org/forum/viewtopic.php?t=%s"}`, topicID)
		case "/api/v2/torrents/trackers":
			io.WriteString(w, `[{"url":"https://bt4.t-ru.org/ann","status":4,"msg":"Torrent not registered"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbitServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"error":{"code":1,"text":"Temporarily disabled"}}`)
	}))
	defer apiServer.Close()

	flaresolverrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"status":"ok","message":"Challenge solved!","solution":{"status":200,"response":"<a class=\"magnet-link\" href=\"magnet:?xt=urn:btih:`+expectedHash+`&amp;dn=release\">download</a>"}}`)
	}))
	defer flaresolverrServer.Close()

	qbit, err := NewQbitClient(qbitServer.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	api := NewRutrackerAPI()
	api.base = apiServer.URL
	updater := &Updater{
		qbit:      qbit,
		rutracker: api,
		resolver: NewRutrackerForumResolver(
			"https://rutracker.org/forum",
			flaresolverrServer.URL,
		),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		dryRun: true,
	}

	plans, err := updater.detectAndPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	if plans[0].topicID != topicID {
		t.Fatalf("topic id = %q, want %q", plans[0].topicID, topicID)
	}
	if !strings.EqualFold(plans[0].expectedHash, expectedHash) {
		t.Fatalf("expected hash = %q, want %q", plans[0].expectedHash, expectedHash)
	}
	if !strings.HasPrefix(plans[0].magnet, "magnet:?xt=urn:btih:"+expectedHash) {
		t.Fatalf("unexpected magnet: %q", plans[0].magnet)
	}
}

func TestApplyPlanConfirmsExpectedHashBeforeDeletingOldTorrents(t *testing.T) {
	t.Parallel()

	const expectedHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var added atomic.Bool
	var identityPersisted atomic.Bool
	var deletedOld atomic.Int32
	qbitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			if !added.Load() {
				io.WriteString(w, `[]`)
				return
			}
			fmt.Fprintf(w, `[{"hash":%q,"name":"replacement","save_path":"/downloads","category":"tv","tags":"tracked, qbit-redownloader-staging","state":"downloading","total_size":100}]`, expectedHash)
		case "/api/v2/torrents/add":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if !hasTag(r.Form.Get("tags"), topicTag("123")) {
				t.Errorf("add tags %q do not preserve topic identity", r.Form.Get("tags"))
			}
			added.Store(true)
		case "/api/v2/torrents/delete":
			if !added.Load() {
				t.Error("old torrent deleted before replacement was added")
			}
			if !identityPersisted.Load() {
				t.Error("old torrent deleted before topic identity was persisted")
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("deleteFiles") != "false" {
				t.Errorf("deleteFiles = %q, want false", r.Form.Get("deleteFiles"))
			}
			deletedOld.Add(1)
		case "/api/v2/torrents/setComment":
			identityPersisted.Store(true)
		case "/api/v2/torrents/properties":
			io.WriteString(w, `{"comment":"https://rutracker.org/forum/viewtopic.php?t=123"}`)
		case "/api/v2/torrents/removeTags":
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbitServer.Close()

	qbit, err := NewQbitClient(qbitServer.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	oldOne := Torrent{Hash: strings.Repeat("1", 40), Name: "old-1", SavePath: "/downloads", Category: "tv", Tags: "tracked"}
	oldTwo := Torrent{Hash: strings.Repeat("2", 40), Name: "old-2", SavePath: "/downloads", Category: "tv", Tags: "tracked"}
	updater := &Updater{
		qbit:           qbit,
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		confirmTimeout: time.Second,
	}
	plan := updatePlan{
		torrent:      oldOne,
		torrents:     []Torrent{oldOne, oldTwo},
		topicID:      "123",
		expectedHash: expectedHash,
		magnet:       "magnet:?xt=urn:btih:" + expectedHash,
	}

	if err := updater.applyPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got := deletedOld.Load(); got != 2 {
		t.Fatalf("deleted %d old torrents, want 2", got)
	}
}

func TestTopicIDFromPersistentTag(t *testing.T) {
	t.Parallel()
	if got := topicIDFromTags("sonarr, rutracker-topic-6875876, imported"); got != "6875876" {
		t.Fatalf("topic id = %q, want 6875876", got)
	}
}

func TestApplyPlanKeepsOldTorrentWhenAddIsNotConfirmed(t *testing.T) {
	t.Parallel()

	const expectedHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var oldDeleted atomic.Bool
	qbitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			io.WriteString(w, `[]`)
		case "/api/v2/torrents/add":
			// qBittorrent commonly returns 200 without returning the added hash.
		case "/api/v2/torrents/delete":
			oldDeleted.Store(true)
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbitServer.Close()

	qbit, err := NewQbitClient(qbitServer.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	old := Torrent{Hash: strings.Repeat("1", 40), Name: "old", SavePath: "/downloads", Category: "tv"}
	updater := &Updater{
		qbit:           qbit,
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		confirmTimeout: 20 * time.Millisecond,
	}
	plan := updatePlan{
		torrent:      old,
		torrents:     []Torrent{old},
		topicID:      "123",
		expectedHash: expectedHash,
		magnet:       "magnet:?xt=urn:btih:" + expectedHash,
	}

	if err := updater.applyPlan(context.Background(), plan); err == nil {
		t.Fatal("applyPlan succeeded without observing the expected hash")
	}
	if oldDeleted.Load() {
		t.Fatal("old torrent was deleted even though replacement was not confirmed")
	}
}

func TestFallbackCachesExactTopicAndGroupsCompatibleDuplicates(t *testing.T) {
	t.Parallel()

	const (
		topicID       = "6866402"
		firstOldHash  = "1111111111111111111111111111111111111111"
		secondOldHash = "2222222222222222222222222222222222222222"
		expectedHash  = "0BD1944DE37259066851BE5058D91CF1F8555E26"
	)

	qbitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			fmt.Fprintf(w, `[
				{"hash":%q,"name":"release-v1","save_path":"/downloads","category":"tv","tags":"tracked"},
				{"hash":%q,"name":"release-v2","save_path":"/downloads","category":"tv","tags":"tracked"}
			]`, firstOldHash, secondOldHash)
		case "/api/v2/torrents/properties":
			fmt.Fprintf(w, `{"comment":"https://rutracker.org/forum/viewtopic.php?t=%s"}`, topicID)
		case "/api/v2/torrents/trackers":
			io.WriteString(w, `[{"url":"https://bt4.t-ru.org/ann","status":4,"msg":"Torrent not registered"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbitServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"error":{"code":1,"text":"Temporarily disabled"}}`)
	}))
	defer apiServer.Close()

	var resolverCalls atomic.Int32
	flaresolverrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resolverCalls.Add(1)
		io.WriteString(w, `{"status":"ok","solution":{"status":200,"response":"<a href=\"magnet:?xt=urn:btih:`+expectedHash+`\">download</a>"}}`)
	}))
	defer flaresolverrServer.Close()

	qbit, err := NewQbitClient(qbitServer.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	api := NewRutrackerAPI()
	api.base = apiServer.URL
	updater := &Updater{
		qbit:      qbit,
		rutracker: api,
		resolver:  NewRutrackerForumResolver("https://rutracker.org/forum", flaresolverrServer.URL),
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		dryRun:    true,
	}

	plans, err := updater.detectAndPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d grouped plans, want 1", len(plans))
	}
	if len(plans[0].torrents) != 2 {
		t.Fatalf("got %d old torrents in grouped plan, want 2", len(plans[0].torrents))
	}
	if calls := resolverCalls.Load(); calls != 1 {
		t.Fatalf("resolver called %d times for one topic id, want 1", calls)
	}
}
