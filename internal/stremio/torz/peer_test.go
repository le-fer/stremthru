package stremio_torz

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/magnet_cache"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/server"
	"github.com/MunifTanjim/stremthru/internal/shared"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tss "github.com/MunifTanjim/stremthru/internal/torrent_stream/torrent_stream_syncinfo"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type peerTestTransport func(*http.Request) (*http.Response, error)

func (f peerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func peerTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func setupPeerTestDB(t *testing.T) {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
}
func newPeerTestHash() string { return strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678" }

func TestPeerPullCandidateConsistency(t *testing.T) {
	t.Run("primary", func(t *testing.T) { testPeerPullCandidateConsistency(t, false) })
	t.Run("lagging replica", func(t *testing.T) { testPeerPullCandidateConsistency(t, true) })
}

func testPeerPullCandidateConsistency(t *testing.T, laggingReplica bool) {
	setupPeerTestDB(t)
	// Unique across -count runs: the pull cache outlives a test database.
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	_, seedErr := db.Exec("INSERT INTO imdb_title (tid, title, orig_title, year, type, is_adult) VALUES (?, 'Diagnostic Programme', '', 0, 'tvSeries', ?)", sid, false)
	require.NoError(t, seedErr)
	correct, wrong := newPeerTestHash(), newPeerTestHash()
	body, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{
		{Hash: correct, TorrentTitle: "Diagnostic.Programme.S01E02.1080p", Source: ti.TorrentInfoSourceTorrentio},
		{Hash: wrong, TorrentTitle: "Diagnostic.Programme.S01E03.1080p", Source: ti.TorrentInfoSourceTorrentio},
	}}})
	require.NoError(t, err)
	pulls := 0
	oldPeer, oldLazy := buddy.PullPeer, torzLazyPull
	buddy.PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: peerTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, sid, r.URL.Query().Get("sid"))
		pulls++
		return peerTestResponse(string(body)), nil
	})}})
	torzLazyPull = false
	t.Cleanup(func() { buddy.PullPeer, torzLazyPull = oldPeer, oldLazy })

	// Replace the idle singleton with a fresh client; reflection saves/restores
	// the entire value without copying mutexes through ordinary assignments.
	// These tests must not run in parallel with store requests.
	rd := shared.GetStore("realdebrid").(*realdebrid.StoreClient)
	original := reflect.New(reflect.TypeOf(rd).Elem()).Elem()
	original.Set(reflect.ValueOf(rd).Elem())
	mock := realdebrid.NewStoreClient(&realdebrid.StoreClientConfig{HTTPClient: &http.Client{Transport: peerTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/rest/1.0/user", r.URL.Path, "unexpected provider request")
		return peerTestResponse(`{"id":1,"premium":3600,"type":"premium"}`), nil
	})}})
	reflect.ValueOf(rd).Elem().Set(reflect.ValueOf(mock).Elem())
	t.Cleanup(func() { reflect.ValueOf(rd).Elem().Set(original) })
	// Availability is seeded locally and fileless. An uncached answer also avoids
	// conflating candidate selection with the cache's episode/file SID filter.
	for _, hash := range []string{correct, wrong} {
		magnet_cache.Touch(store.StoreCodeRealDebrid, hash, nil, false, true)
	}

	if laggingReplica {
		// Availability is pre-seeded separately from candidate lookup.
		_, err := magnet_cache.GetByHashes(store.StoreCodeRealDebrid, []string{correct, wrong}, "")
		require.NoError(t, err)
		replica, err := sql.Open("sqlite3", ":memory:")
		require.NoError(t, err)
		replica.SetMaxOpenConns(1)
		require.NoError(t, goose.Up(replica, "../../../migrations/sqlite"))
		t.Cleanup(func() { replica.Close() })
		db.SetReplicasForTesting([]*sql.DB{replica}, t.Cleanup)
	}

	request := func() []string {
		r := httptest.NewRequest("GET", "http://torz.invalid/stream", nil)
		r.SetPathValue("userData", util.Base64Encode(`{"stores":[{"c":"rd","t":"diagnostic-token"}]}`))
		r.SetPathValue("contentType", "series")
		r.SetPathValue("id", sid+":1:2")
		r = server.SetReqCtx(r, &server.ReqCtx{Log: log})
		w := httptest.NewRecorder()
		handleStream(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		var result struct {
			Streams []struct {
				URL string `json:"url"`
			} `json:"streams"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		hashes := []string{}
		for _, stream := range result.Streams {
			for _, hash := range []string{correct, wrong} {
				if strings.Contains(stream.URL, hash) {
					hashes = append(hashes, hash)
				}
			}
		}
		return hashes
	}
	first := request()
	assert.Equal(t, []string{correct}, first)
	require.Eventually(t, func() bool { return !tss.ShouldPull(sid) }, time.Second, time.Millisecond)
	assert.Equal(t, first, request())
	assert.Equal(t, 1, pulls)
	candidates, err := ti.ListHashesByStremIdFromPrimary(sid + ":1:2")
	require.NoError(t, err)
	assert.Equal(t, []string{correct}, candidates)
}

func TestGetStreamsForHashesEpisodeRelevance(t *testing.T) {
	setupPeerTestDB(t)
	sid := "tt1234567:1:2"
	nsid, err := ts.NormalizeStreamId(sid)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, title, fileSID, fileName string
		want                           int
	}{
		{name: "matching fileless", title: "Diagnostic.Programme.S01E02", want: 1},
		{name: "wrong episode", title: "Diagnostic.Programme.S01E03"},
		{name: "wrong season", title: "Diagnostic.Programme.S02E02"},
		{name: "season pack", title: "Diagnostic.Programme.S01", want: 1},
		{name: "complete series pack", title: "Diagnostic.Programme.Complete.1080p", want: 1},
		{name: "unknown fileless", title: "Diagnostic.Programme.1080p", want: 1},
		{name: "explicit file wins", title: "Diagnostic.Programme.S02E03", fileSID: sid, fileName: "video.mkv", want: 1},
		{name: "file name wins", title: "Diagnostic.Programme.S02E03", fileName: "Diagnostic.Programme.S01E02.mkv", want: 1},
		{name: "wrong files", title: "Diagnostic.Programme.S01", fileName: "Diagnostic.Programme.S01E03.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := newPeerTestHash()
			require.NoError(t, ti.Upsert([]ti.TorrentInfoInsertData{{Hash: hash, TorrentTitle: tc.title, Source: ti.TorrentInfoSourceTorrentio}}, "", false))
			if tc.fileName != "" {
				require.NoError(t, ts.Record([]ts.InsertData{{Hash: hash, File: ts.File{Path: "/" + tc.fileName, Name: tc.fileName, SId: tc.fileSID, Source: "tio"}}}, false))
			}
			streams, err := GetStreamsForHashes("series", sid, []string{hash}, nsid)
			require.NoError(t, err)
			assert.Len(t, streams, tc.want)
		})
	}
}

func TestGetStreamsForHashesKeepsMovieAndAnimeBehavior(t *testing.T) {
	setupPeerTestDB(t)
	hash := newPeerTestHash()
	require.NoError(t, ti.Upsert([]ti.TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Title.S02E03", Source: ti.TorrentInfoSourceTorrentio}}, "", false))
	for _, nsid := range []*ts.NormalizedStremId{
		{Id: "tt1234567"},
		{IsAnime: true, Id: "12345", Season: "1", Episode: "2"},
	} {
		streams, err := GetStreamsForHashes("series", nsid.String(), []string{hash}, nsid)
		require.NoError(t, err)
		assert.Len(t, streams, 1, "anime episode conversions must not be replaced by TV title filtering")
	}
}
