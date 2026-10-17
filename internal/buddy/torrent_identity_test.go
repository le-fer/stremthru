package buddy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/stretchr/testify/require"
)

func TestPeerPullRetriesUntilIMDbIdentityIsAvailable(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	valid := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	wrong := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	items := []ti.TorrentItem{
		{Hash: valid, TorrentTitle: "Diagnostic.Programme.S02E03.1080p", Source: ti.TorrentInfoSourceTorrentio},
		{Hash: wrong, TorrentTitle: "Unrelated.Collection.Complete.1080p", Source: ti.TorrentInfoSourceTorrentio},
	}
	payload, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: items}})
	require.NoError(t, err)
	original := PullPeer
	calls := 0
	PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: torrentTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}})
	t.Cleanup(func() { PullPeer = original })
	// The first request precedes IMDb's initial metadata sync.
	_, err = pullTorrentsByStremId(sid, "")
	require.ErrorContains(t, err, "IMDb title metadata unavailable")
	require.True(t, tss.ShouldPull(sid), "an incomplete identity check must not consume the successful-pull cooldown")
	stored, err := ti.GetByHashesFromPrimary([]string{valid, wrong})
	require.NoError(t, err)
	require.Len(t, stored, 2)
	require.True(t, stored[valid].IsParsed())
	// Metadata becomes available without restarting or clearing caches.
	_, err = db.Exec("INSERT INTO imdb_title (tid,title,orig_title,year,type,is_adult) VALUES (?, 'Diagnostic Programme', '', 2020, 'tvSeries', ?)", sid, false)
	require.NoError(t, err)
	_, err = pullTorrentsByStremId(sid, "")
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	hashes, err := ti.ListHashesByStremIdFromPrimary(sid + ":2:3")
	require.NoError(t, err)
	require.Equal(t, []string{valid}, hashes, "metadata readiness restores valid results while retaining wrong-title rejection")
	require.False(t, tss.ShouldPull(sid))
	_, err = pullTorrentsByStremId(sid, "")
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}
