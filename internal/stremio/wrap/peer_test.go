package stremio_wrap

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/magnet_cache"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/server"
	stremio_userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type wrapPeerTransport func(*http.Request) (*http.Response, error)

func (f wrapPeerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func wrapPeerResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
func TestIncludeTorzAfterPullWithLaggingReplica(t *testing.T) {
	primary, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	primary.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(primary, "../../../migrations/sqlite"))
	db.SetForTesting(primary, t.Cleanup)
	sid := fmt.Sprintf("tt%d", time.Now().UnixNano())
	_, seedErr := db.Exec("INSERT INTO imdb_title (tid, title, orig_title, year, type, is_adult) VALUES (?, 'Diagnostic Programme', '', 0, 'tvSeries', ?)", sid, false)
	require.NoError(t, seedErr)
	hash := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	magnet_cache.Touch(store.StoreCodeRealDebrid, hash, nil, false, true)
	// Isolate replica lag in the candidate path from provider availability.
	_, err = magnet_cache.GetByHashes(store.StoreCodeRealDebrid, []string{hash}, "")
	require.NoError(t, err)
	replica, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	replica.SetMaxOpenConns(1)
	require.NoError(t, goose.Up(replica, "../../../migrations/sqlite"))
	t.Cleanup(func() { replica.Close() })
	db.SetReplicasForTesting([]*sql.DB{replica}, t.Cleanup)
	body, err := json.Marshal(peer.Response[ti.ListTorrentsData]{Data: ti.ListTorrentsData{Items: []ti.TorrentItem{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: ti.TorrentInfoSourceTorrentio}}}})
	require.NoError(t, err)
	calls := 0
	oldPeer, oldLazy := buddy.PullPeer, lazyPullTorz
	buddy.PullPeer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://peer.invalid", HTTPClient: &http.Client{Transport: wrapPeerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return wrapPeerResponse(body), nil
	})}})
	lazyPullTorz = false
	t.Cleanup(func() { buddy.PullPeer, lazyPullTorz = oldPeer, oldLazy })
	ctx := &Ctx{Log: log}
	ud := UserData{IncludeTorz: true, resolver: upstreamsResolver{}, UserDataStores: stremio_userdata.UserDataStores{Stores: []stremio_userdata.Store{{Code: "rd", Token: "diagnostic-token"}}}}
	prepareErr, _ := ud.UserDataStores.Prepare(ctx)
	require.NoError(t, prepareErr)
	ud.GetStores()[0].Store = realdebrid.NewStoreClient(&realdebrid.StoreClientConfig{HTTPClient: &http.Client{Transport: wrapPeerTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/rest/1.0/user", r.URL.Path, "unexpected provider request")
		return wrapPeerResponse([]byte(`{"id":1,"premium":3600,"type":"premium"}`)), nil
	})}})
	r := server.SetReqCtx(httptest.NewRequest("GET", "http://wrap.invalid/stream", nil), &server.ReqCtx{Log: log})
	first, err := ud.fetchStream(ctx, r, "series", sid+":1:2.json")
	require.NoError(t, err)
	require.Len(t, first.Streams, 1)
	require.Contains(t, first.Streams[0].URL, hash)
	repeated, err := ud.fetchStream(ctx, r, "series", sid+":1:2.json")
	require.NoError(t, err)
	require.Equal(t, first.Streams, repeated.Streams)
	wrong, err := ud.fetchStream(ctx, r, "series", sid+":1:3.json")
	require.NoError(t, err)
	require.Empty(t, wrong.Streams)
	require.Equal(t, 1, calls)
}
