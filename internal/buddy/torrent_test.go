package buddy

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/peer"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var pullTorrentTestSequence atomic.Uint64

type pullTorrentRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pullTorrentRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func setupPullTorrentTestDB(t *testing.T) {
	t.Helper()
	torrent_stream.ResetCachesForTesting(t.Cleanup)
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	db.SetForTesting(sqlDB, t.Cleanup)
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, filepath.Join(filepath.Dir(source), "../../migrations/sqlite")))
}

func pullTorrentJSONResponse(r *http.Request, status int, value any) (*http.Response, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    r,
	}, nil
}

func TestPullTorrentsByStremIdFiltersAndAlignsProviderHashes(t *testing.T) {
	setupPullTorrentTestDB(t)
	previousPeer, previousLocalOnly, previousNoTorrentInfo := PullPeer, pullLocalOnly, noTorrentInfo
	pullLocalOnly, noTorrentInfo = true, false
	t.Cleanup(func() {
		PullPeer, pullLocalOnly, noTorrentInfo = previousPeer, previousLocalOnly, previousNoTorrentInfo
	})

	sequence := pullTorrentTestSequence.Add(1)
	firstHash := fmt.Sprintf("%040x", sequence*2)
	secondHash := fmt.Sprintf("%040x", sequence*2+1)
	v2 := strings.Repeat("ef", 32)
	sid := fmt.Sprintf("tt9%07d", sequence)
	items := []ti.TorrentItem{
		{Hash: strings.ToUpper(firstHash), TorrentTitle: "First Movie", Source: ti.TorrentInfoSourceTorBox},
		{Hash: "malformed", TorrentTitle: "Wrong Movie", Source: ti.TorrentInfoSourceTorBox},
		{Hash: v2, TorrentTitle: "Unsupported V2", Source: ti.TorrentInfoSourceTorBox},
		{Hash: strings.ToUpper(secondHash), TorrentTitle: "Second Movie", Source: ti.TorrentInfoSourceTorBox},
	}
	PullPeer = peer.NewAPIClient(&peer.APIClientConfig{
		BaseURL: "https://peer.fixture.invalid",
		HTTPClient: &http.Client{Transport: pullTorrentRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, "/v0/torrents", r.URL.Path)
			require.Equal(t, sid, r.URL.Query().Get("sid"))
			require.Equal(t, "1", r.URL.Query().Get("local_only"))
			require.Equal(t, "origin-fixture", r.Header.Get("X-StremThru-Origin-Instance-Id"))
			return pullTorrentJSONResponse(r, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "total_items": len(items)}})
		})},
	})

	hashes := PullTorrentsByStremId(sid, "origin-fixture")
	require.Equal(t, []string{firstHash, secondHash}, hashes)
	for _, tc := range []struct {
		hash  string
		title string
	}{
		{hash: firstHash, title: "First Movie"},
		{hash: secondHash, title: "Second Movie"},
	} {
		info, err := ti.GetByHash(tc.hash)
		require.NoError(t, err)
		require.NotNil(t, info)
		require.Equal(t, tc.title, info.TorrentTitle, "filtering must not misalign metadata with hashes")
	}
	for _, hash := range []string{"malformed", v2} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", hash).Scan(&count))
		require.Zero(t, count)
	}
	require.Eventually(t, func() bool {
		var count int
		return db.QueryRow("SELECT COUNT(*) FROM torrent_stream_syncinfo WHERE sid = ?", sid).Scan(&count) == nil && count == 1
	}, time.Second, 10*time.Millisecond, "wait for asynchronous pull bookkeeping before restoring the database")
}
