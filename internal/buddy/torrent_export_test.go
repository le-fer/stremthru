package buddy_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/endpoint"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/server"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	tss "github.com/MunifTanjim/stremthru/internal/torrent_stream/torrent_stream_syncinfo"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type exportTransport func(*http.Request) (*http.Response, error)

func (f exportTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func setupExportDB(t *testing.T) {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
}

func requestPeerExport(t *testing.T, sid string, localOnly bool) (int, peer.Response[ti.ListTorrentsData]) {
	t.Helper()
	mux := http.NewServeMux()
	endpoint.AddTorrentEndpoints(mux)
	url := "http://export.invalid/v0/torrents?sid=" + sid
	if localOnly {
		url += "&local_only=1"
	}
	r := server.SetReqCtx(httptest.NewRequest(http.MethodGet, url, nil), &server.ReqCtx{Log: logger.Scoped("peer-export-test")})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code >= 500 {
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"), "failed exports must remain retryable through HTTP caches")
	}
	var response peer.Response[ti.ListTorrentsData]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response), w.Body.String())
	return w.Code, response
}

func TestPeerExportFailuresRemainRetryable(t *testing.T) {
	for _, failure := range []string{"upstream", "metadata", "mapping", "halted"} {
		t.Run(failure, func(t *testing.T) {
			setupExportDB(t)
			sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
			_, seedErr := db.Exec("INSERT INTO imdb_title (tid, title, orig_title, year, type, is_adult) VALUES (?, 'Diagnostic Programme', '', 0, 'tvSeries', ?)", sid, false)
			require.NoError(t, seedErr)
			hash := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
			payload, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: ti.TorrentInfoSourceTorrentio}}}})
			require.NoError(t, err)
			calls := 0
			failUpstream := failure == "upstream"
			conf := &peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: exportTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if failUpstream {
					return nil, errors.New("injected upstream failure")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
			})}}
			original := buddy.PullPeer
			buddy.PullPeer = peer.NewAPIClient(conf)
			t.Cleanup(func() { buddy.PullPeer = original })
			table := ""
			if failure == "metadata" {
				table = "torrent_info"
			}
			if failure == "mapping" {
				table = "imdb_torrent"
			}
			if table != "" {
				_, err = db.Exec("CREATE TRIGGER fail_export_import BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'injected import failure'); END")
				require.NoError(t, err)
			}
			if failure == "halted" {
				buddy.PullPeer.HaltCheckMagnet()
			}
			status, failed := requestPeerExport(t, sid, false)
			require.GreaterOrEqual(t, status, 500)
			require.NotNil(t, failed.Error, "import failures must not become successful empty peer responses")
			require.True(t, tss.ShouldPull(sid), "failure must leave the pull eligible for retry")
			if failure == "halted" {
				require.Zero(t, calls)
			}
			// Explicit local-only requests remain usable while an upstream pull fails.
			status, local := requestPeerExport(t, sid, true)
			require.Equal(t, http.StatusOK, status)
			require.Nil(t, local.Error)
			require.Empty(t, local.Data.Items)

			failUpstream = false
			if table != "" {
				_, err = db.Exec("DROP TRIGGER fail_export_import")
				require.NoError(t, err)
			}
			if failure == "halted" {
				buddy.PullPeer = peer.NewAPIClient(conf)
			}
			status, retried := requestPeerExport(t, sid, false)
			require.Equal(t, http.StatusOK, status)
			require.Nil(t, retried.Error)
			require.Len(t, retried.Data.Items, 1)
			require.Equal(t, hash, retried.Data.Items[0].Hash)
			require.False(t, tss.ShouldPull(sid))
			callsAfterSuccess := calls
			// A valid cooldown result needs no upstream access, even during a halt.
			buddy.PullPeer.HaltCheckMagnet()
			status, repeated := requestPeerExport(t, sid, false)
			require.Equal(t, http.StatusOK, status)
			require.Nil(t, repeated.Error)
			require.Equal(t, retried.Data, repeated.Data)
			require.Equal(t, callsAfterSuccess, calls)
		})
	}
}

func TestPeerExportSuccessfulEmptyPullUsesCooldown(t *testing.T) {
	setupExportDB(t)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	calls := 0
	original := buddy.PullPeer
	buddy.PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: exportTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":{"items":[],"total_items":0}}`))}, nil
	})}})
	t.Cleanup(func() { buddy.PullPeer = original })
	for range 2 {
		status, response := requestPeerExport(t, sid, false)
		require.Equal(t, http.StatusOK, status)
		require.Nil(t, response.Error)
		require.Empty(t, response.Data.Items)
	}
	require.Equal(t, 1, calls)
	require.False(t, tss.ShouldPull(sid))
}

func TestPeerExportDoesNotExposeUpstreamURL(t *testing.T) {
	setupExportDB(t)
	original := buddy.PullPeer
	buddy.PullPeer = peer.NewAPIClient(&peer.APIClientConfig{
		BaseURL: "https://private-user:private-password@peer.invalid/private-token",
		HTTPClient: &http.Client{Transport: exportTransport(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("transport failed with private-inner-secret")
		})},
	})
	t.Cleanup(func() { buddy.PullPeer = original })
	mux := http.NewServeMux()
	endpoint.AddTorrentEndpoints(mux)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	r := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "/v0/torrents?sid="+sid, nil), &server.ReqCtx{Log: logger.Scoped("peer-export-test")})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	require.GreaterOrEqual(t, w.Code, 500)
	require.Contains(t, w.Body.String(), "failed to pull peer torrents")
	for _, secret := range []string{"peer.invalid", "private-user", "private-password", "private-token", "private-inner-secret"} {
		require.NotContains(t, w.Body.String(), secret, "the entire response, including nested causes, must be safe")
	}
	require.True(t, tss.ShouldPull(sid))
}

func TestPeerExportWithoutIMDbSync(t *testing.T) {
	// Feature settings are read at package initialization. Use the same test
	// binary in a child process to exercise the supported disabled-sync setting.
	if config.Feature.HasIMDBTitle() || !config.Feature.HasTorz() {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPeerExportWithoutIMDbSync$")
		cmd.Env = append(os.Environ(), "STREMTHRU_ENV=test", "STREMTHRU_FEATURE=-imdb_title")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	setupExportDB(t)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	mapped := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	unverified := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	payload, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{
		{Hash: mapped, TorrentTitle: "Diagnostic.Movie.2024.1080p", Source: ti.TorrentInfoSourceTorrentio, Files: []ti.TorrentInfoInsertDataFile{{Path: "/Diagnostic.Movie.2024.mkv", SId: sid, Source: "tio"}}},
		{Hash: unverified, TorrentTitle: "Unrelated.Collection.Complete.1080p", Source: ti.TorrentInfoSourceTorrentio},
	}}})
	require.NoError(t, err)
	original := buddy.PullPeer
	calls := 0
	buddy.PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: exportTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}})
	t.Cleanup(func() { buddy.PullPeer = original })
	for range 2 {
		status, result := requestPeerExport(t, sid, false)
		require.Equal(t, http.StatusOK, status)
		require.Nil(t, result.Error)
		require.Len(t, result.Data.Items, 1)
		require.Equal(t, mapped, result.Data.Items[0].Hash)
	}
	require.Equal(t, 1, calls, "disabled optional sync must not cause indefinite peer refetches")
	require.False(t, tss.ShouldPull(sid))
	status, local := requestPeerExport(t, sid, true)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, local.Data.Items, 1)
	var inferred int
	require.NoError(t, db.GetPrimary().QueryRow("SELECT COUNT(*) FROM imdb_torrent WHERE hash IN (?, ?)", mapped, unverified).Scan(&inferred))
	require.Zero(t, inferred, "disabling sync must not bypass identity checks for new whole-title associations")
	stored, err := ti.GetByHashesFromPrimary([]string{mapped, unverified})
	require.NoError(t, err)
	require.Len(t, stored, 2)
	require.True(t, stored[mapped].IsParsed())
	require.True(t, stored[unverified].IsParsed())
}
