package stremio_torz

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	"github.com/MunifTanjim/stremthru/internal/imdb_torrent"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	"github.com/MunifTanjim/stremthru/internal/shared"
	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/torznab/generic"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/stretchr/testify/require"
)

func TestGetCheckMagnetHashesExcludesStrictRejections(t *testing.T) {
	allowed, blocked := strings.Repeat("a", 40), strings.Repeat("b", 40)
	stream := func(hash string) WrappedStream {
		return WrappedStream{R: &stremio_transformer.StreamExtractorResult{Hash: hash}}
	}

	require.Equal(t, []string{allowed}, getCheckMagnetHashes(
		[]WrappedStream{stream(blocked), stream(allowed)},
		[]WrappedStream{stream(strings.ToUpper(allowed)), stream(blocked)},
		map[string]struct{}{blocked: {}},
	))
}

func TestHandleStreamSuppressesInvalidUncachedFallback(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = allow
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
			testutil.SetupDB(t)
			blocked, allowed, unknown := testutil.Hash(t), testutil.Hash(t), testutil.Hash(t)
			for _, hash := range []string{blocked, allowed, unknown} {
				_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
				require.NoError(t, err)
			}
			require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: blocked}, {TId: "tt1234567", Hash: allowed}, {TId: "tt1234567", Hash: unknown}}))
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
				{Hash: blocked, File: torrent_stream.File{Path: "/movie.mkv"}},
				{Hash: blocked, File: torrent_stream.File{Path: "/setup.exe"}},
				{Hash: allowed, File: torrent_stream.File{Path: "/movie.mkv"}},
			}, false))
			for _, hash := range []string{blocked, allowed} {
				_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('rd',?,1,unixepoch())", hash)
				require.NoError(t, err)
			}
			api := shared.GetStore("realdebrid").(*realdebrid.StoreClient).GetClient()
			previousAPI, previousPeer, previousPull, previousLazy := api.HTTPClient, config.HasPeer, buddy.PullPeer, torzLazyPull
			config.HasPeer, buddy.PullPeer, torzLazyPull = false, nil, false
			t.Cleanup(func() {
				api.HTTPClient, config.HasPeer, buddy.PullPeer, torzLazyPull = previousAPI, previousPeer, previousPull, previousLazy
			})
			api.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Contains(t, r.URL.Path, "/user")
				return testutil.JSONResponse(r, 200, map[string]any{"id": 1, "premium": 3600}), nil
			})}
			req := httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil)
			req.SetPathValue("userData", util.Base64Encode(`{"stores":[{"c":"rd","t":"`+unknown+`"}]}`))
			req.SetPathValue("contentType", "movie")
			req.SetPathValue("idJson", "tt1234567.json")
			req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("test")})
			response := httptest.NewRecorder()
			handleStream(response, req)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var result stremio.StreamHandlerResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			want := 2
			if allow {
				want = 3
			}
			require.Len(t, result.Streams, want)
			var hasAllowed, hasUnknown bool
			for _, stream := range result.Streams {
				if !allow {
					require.NotContains(t, stream.URL, blocked)
				}
				hasAllowed = hasAllowed || strings.Contains(stream.URL, allowed)
				hasUnknown = hasUnknown || strings.Contains(stream.URL, unknown)
			}
			require.True(t, hasAllowed)
			require.True(t, hasUnknown)
		})
	}
}

func TestHandleStreamDoesNotReloadNewIndexerHash(t *testing.T) {
	testutil.SetupDB(t)
	hash, sid := testutil.Hash(t), "tt1234567"
	require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: sid, Title: "Movie", Year: 2024, Type: "movie"}}))
	oldPeer, oldBuddy, oldPull, oldLazy := config.HasPeer, config.HasBuddy, buddy.PullPeer, torzLazyPull
	config.HasPeer, config.HasBuddy, buddy.PullPeer, torzLazyPull = false, false, nil, false
	t.Cleanup(func() {
		config.HasPeer, config.HasBuddy, buddy.PullPeer, torzLazyPull = oldPeer, oldBuddy, oldPull, oldLazy
	})
	ud := UserData{UserDataIndexers: userdata.UserDataIndexers{Indexers: []userdata.Indexer{{Name: userdata.IndexerNameGeneric, URL: "https://" + hash + ".invalid"}}}, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "rd", Token: hash}}}}
	indexers, err := ud.UserDataIndexers.Prepare()
	require.NoError(t, err)
	client := indexers[0].(*generic.TorznabClient)
	oldIndexer := client.HTTPClient
	t.Cleanup(func() { client.HTTPClient = oldIndexer })
	client.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		xml := `<caps><server title="fixture"/><searching><movie-search available="yes" supportedParams="imdbid"/></searching></caps>`
		if r.URL.Query().Get("t") != "caps" {
			xml = `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Movie.2024.1080p</title><enclosure url="magnet:?xt=urn:btih:` + hash + `" length="100" type="application/x-bittorrent"/><torznab:attr name="infohash" value="` + hash + `"/></item></channel></rss>`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(xml)), Request: r}, nil
	})}
	api := shared.GetStore("realdebrid").(*realdebrid.StoreClient).GetClient()
	oldAPI := api.HTTPClient
	t.Cleanup(func() { api.HTTPClient = oldAPI })
	api.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Contains(t, r.URL.Path, "/user")
		// Force the indexer's asynchronous upsert to finish before stored lookup.
		require.Eventually(t, func() bool {
			var count int
			err := db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", hash).Scan(&count)
			return err == nil && count == 1
		}, 5*time.Second, 10*time.Millisecond)
		return testutil.JSONResponse(r, 200, map[string]any{"id": 1, "premium": 3600}), nil
	})}
	blob, err := json.Marshal(ud)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil)
	req.SetPathValue("userData", util.Base64Encode(string(blob)))
	req.SetPathValue("contentType", "movie")
	req.SetPathValue("idJson", sid+".json")
	req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("test")})
	response := httptest.NewRecorder()
	handleStream(response, req)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result stremio.StreamHandlerResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Streams, 1, "an indexer upsert must not create a second stored result in the same request")
	require.Contains(t, result.Streams[0].URL, hash)
}
