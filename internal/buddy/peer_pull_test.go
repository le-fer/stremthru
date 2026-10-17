package buddy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/peer"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	tss "github.com/MunifTanjim/stremthru/internal/torrent_stream/torrent_stream_syncinfo"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type torrentTestTransport func(*http.Request) (*http.Response, error)

func (f torrentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPullTorrentsRetriesFailedImport(t *testing.T) {
	var logs bytes.Buffer
	originalLog := pullPeerLog.L
	pullPeerLog.L = slog.New(slog.NewJSONHandler(&logs, nil))
	t.Cleanup(func() { pullPeerLog.L = originalLog })
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	_, seedErr := db.Exec("INSERT INTO imdb_title (tid, title, orig_title, year, type, is_adult) VALUES (?, 'Diagnostic Programme', '', 0, 'tvSeries', ?)", sid, false)
	require.NoError(t, seedErr)
	hash := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	payload, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: ti.TorrentInfoSourceTorrentio}}}})
	require.NoError(t, err)
	old := PullPeer
	calls := 0
	PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: torrentTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}})
	t.Cleanup(func() { PullPeer = old })
	for _, table := range []string{"torrent_info", "imdb_torrent"} {
		t.Run(table, func(t *testing.T) {
			_, err := db.Exec("CREATE TRIGGER fail_import BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'import failed'); END")
			require.NoError(t, err)
			assert.Empty(t, PullTorrentsByStremId(sid, ""))
			assert.True(t, tss.ShouldPull(sid), "failed import must not consume the cooldown")
			var marks int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream_syncinfo WHERE sid = ?", sid).Scan(&marks))
			assert.Zero(t, marks)
			_, err = db.Exec("DROP TRIGGER fail_import")
			require.NoError(t, err)
		})
	}
	assert.Equal(t, []string{hash}, PullTorrentsByStremId(sid, ""))
	assert.False(t, tss.ShouldPull(sid), "successful import must finish bookkeeping before returning")
	assert.Empty(t, PullTorrentsByStremId(sid, ""))
	assert.Equal(t, 3, calls)
	hashes, err := ti.ListHashesByStremId(sid + ":1:2")
	require.NoError(t, err)
	assert.Equal(t, []string{hash}, hashes)
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record struct {
			Message        string `json:"msg"`
			Duration       int64  `json:"duration"`
			FetchDuration  int64  `json:"fetch_duration"`
			ImportDuration int64  `json:"import_duration"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		require.Positive(t, record.FetchDuration)
		require.Positive(t, record.ImportDuration, record.Message)
		require.GreaterOrEqual(t, record.Duration, record.FetchDuration+record.ImportDuration, record.Message)
	}
}

func TestPullTorrentsLogsDoNotExposeUpstreamSecrets(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	_, err = conn.Exec("CREATE TABLE torrent_stream_syncinfo (sid TEXT PRIMARY KEY, pulled_at TEXT)")
	require.NoError(t, err)
	db.SetForTesting(conn, t.Cleanup)
	var logs bytes.Buffer
	originalLog, originalPeer := pullPeerLog.L, PullPeer
	pullPeerLog.L = slog.New(slog.NewJSONHandler(&logs, nil))
	t.Cleanup(func() { pullPeerLog.L, PullPeer = originalLog, originalPeer })
	for _, tc := range []struct {
		name, category string
		status         int
		transportError error
	}{
		{name: "transport", category: "transport", transportError: errors.New("transport failed with private-inner-secret")},
		{name: "dns", category: "dns", transportError: &net.DNSError{Err: "private-inner-secret", Name: "peer.invalid"}},
		{name: "timeout", category: "timeout", transportError: context.DeadlineExceeded},
		{name: "canceled", category: "canceled", transportError: context.Canceled},
		{name: "http", category: "http", status: http.StatusServiceUnavailable},
		{name: "response", category: "response", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			PullPeer = peer.NewAPIClient(&peer.APIClientConfig{
				BaseURL: "https://private-user:private-password@peer.invalid/private-token",
				HTTPClient: &http.Client{Transport: torrentTestTransport(func(r *http.Request) (*http.Response, error) {
					if tc.transportError != nil {
						return nil, tc.transportError
					}
					return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("private-inner-secret"))}, nil
				})},
			})
			_, err = pullTorrentsByStremId(fmt.Sprintf("tt%d", time.Now().UnixNano()), "")
			require.Error(t, err)
			for _, secret := range []string{"peer.invalid", "private-user", "private-password", "private-token", "private-inner-secret"} {
				require.NotContains(t, logs.String(), secret)
				require.NotContains(t, err.Error(), secret)
			}
			var record struct {
				Message  string `json:"msg"`
				Category string `json:"error_category"`
				Status   int    `json:"status_code"`
				Timeout  bool   `json:"timeout"`
			}
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record))
			require.Equal(t, "failed to pull torrents", record.Message)
			require.Equal(t, tc.category, record.Category)
			require.Equal(t, tc.status, record.Status)
			require.Equal(t, tc.category == "timeout", record.Timeout)
			require.Contains(t, logs.String(), "error_type")
			require.Contains(t, logs.String(), "fetch_duration")
		})
	}
}

func TestPeerExportAfterPullWithLaggingReplica(t *testing.T) {
	primary, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	primary.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(primary, "../../migrations/sqlite"))
	db.SetForTesting(primary, t.Cleanup)
	replica, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	replica.SetMaxOpenConns(1)
	require.NoError(t, goose.Up(replica, "../../migrations/sqlite"))
	t.Cleanup(func() { replica.Close() })
	db.SetReplicasForTesting([]*sql.DB{replica}, t.Cleanup)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	_, seedErr := db.Exec("INSERT INTO imdb_title (tid, title, orig_title, year, type, is_adult) VALUES (?, 'Diagnostic Programme', '', 0, 'tvSeries', ?)", sid, false)
	require.NoError(t, seedErr)
	hash := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	body, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Size: 4096, Source: ti.TorrentInfoSourceTorrentio}}}})
	require.NoError(t, err)
	calls := 0
	original := PullPeer
	PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: torrentTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}})
	t.Cleanup(func() { PullPeer = original })
	result, err := ListTorrentsByStremId(sid, false, "review-origin", false)
	require.NoError(t, err)
	imported, err := ti.GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.True(t, imported[hash].IsParsed(), "import completed on primary")
	require.Len(t, result.Items, 1, "peer response must include the newly imported torrent despite replica lag")
	require.Equal(t, hash, result.Items[0].Hash)
	require.Equal(t, 1, result.TotalItems)
	// Repeat exports must use the same durable data after the pull cooldown.
	for _, localOnly := range []bool{false, true} {
		result, err = ListTorrentsByStremId(sid+":1:2", localOnly, "review-origin", true)
		require.NoError(t, err)
		require.Len(t, result.Items, 1)
		require.Equal(t, hash, result.Items[0].Hash)
		result, err = ListTorrentsByStremId(sid+":1:3", localOnly, "review-origin", false)
		require.NoError(t, err)
		require.Empty(t, result.Items, "peer export must still enforce episode relevance")
	}
	require.Equal(t, 1, calls)
	// The ordinary listing API continues to use the configured replica.
	replicaResult, err := ti.ListByStremId(sid, false)
	require.NoError(t, err)
	require.Empty(t, replicaResult.Items)
}
