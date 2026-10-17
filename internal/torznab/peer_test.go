package torznab

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/peer"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type searchPeerTransport func(*http.Request) (*http.Response, error)

func (f searchPeerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSearchAfterPullWithLaggingReplica(t *testing.T) {
	previousLazy := config.PeerFlag.Lazy
	config.PeerFlag.Lazy = false
	t.Cleanup(func() { config.PeerFlag.Lazy = previousLazy })
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
	body, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: ti.TorrentInfoSourceTorrentio}}}})
	require.NoError(t, err)
	calls := 0
	original := buddy.PullPeer
	buddy.PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: searchPeerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}})
	t.Cleanup(func() { buddy.PullPeer = original })
	result, err := (stremThruIndexer{}).Search(Query{IMDBId: sid, Season: "1", Ep: "2"})
	require.NoError(t, err)
	imported, err := ti.GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.True(t, imported[hash].IsParsed(), "import completed on primary")
	require.Len(t, result, 1, "search must include the newly imported torrent despite replica lag")
	require.Equal(t, hash, result[0].InfoHash)
	result, err = (stremThruIndexer{}).Search(Query{IMDBId: sid, Season: "1", Ep: "2"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	result, err = (stremThruIndexer{}).Search(Query{IMDBId: sid, Season: "1", Ep: "3"})
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, 1, calls)
}
