package stremio_wrap

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_torrent"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type selectedPathStore struct {
	store.Store
	hash   string
	cached bool
	files  []store.MagnetFile
}

func (selectedPathStore) GetName() store.StoreName { return store.StoreNameTorBox }
func (s selectedPathStore) CheckMagnet(*store.CheckMagnetParams) (*store.CheckMagnetData, error) {
	status := store.MagnetStatus("")
	if s.cached {
		status = store.MagnetStatusCached
	}
	return &store.CheckMagnetData{Items: []store.CheckMagnetDataItem{{Hash: s.hash, Status: status, Files: s.files}}}, nil
}

func TestWrapTorzSelectedPathSurvivesPlaybackURL(t *testing.T) {
	const sid = "tt9998401:1:2"
	const selectedPath = "/Season 1/Version A + # & ? % ü/E02.mkv"
	const otherPath = "/Season 1/Version B/E02.mkv"
	storedFiles := torrent_stream.Files{
		{Path: selectedPath, Name: "E02.mkv", Idx: 1, SId: "*"},
		{Path: otherPath, Name: "E02.mkv", Idx: 2, SId: "*"},
	}
	for _, tc := range []struct {
		name     string
		cached   bool
		files    torrent_stream.Files
		upstream bool
	}{
		{"initial cached result", true, storedFiles, false},
		{"initial uncached result", false, storedFiles, false},
		{"reconfigured cached upstream", true, storedFiles, true},
		{"reconfigured uncached upstream", false, storedFiles, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, err := sql.Open("sqlite3", ":memory:")
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			goose.SetLogger(goose.NopLogger())
			require.NoError(t, goose.SetDialect("sqlite"))
			require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
			db.SetForTesting(sqlDB, t.Cleanup)
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
			// Keep the local Torz source empty for upstream cases. Including it
			// makes peer pulling synchronous, so cleanup cannot race that task.
			if !tc.upstream {
				require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Synthetic.Series.S01.1080p", Size: 1000, Source: torrent_info.TorrentInfoSourceTorrentio, Files: tc.files}}, "", false))
				_, err = db.Exec("UPDATE torrent_info SET seasons = '1' WHERE hash = ?", hash)
				require.NoError(t, err)
				require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt9998401", Hash: hash}}))
			}

			oldPull, oldLazy, oldPeer := buddy.PullPeer, lazyPullTorz, config.HasPeer
			buddy.PullPeer, lazyPullTorz, config.HasPeer = nil, false, false
			t.Cleanup(func() { buddy.PullPeer, lazyPullTorz, config.HasPeer = oldPull, oldLazy, oldPeer })
			ud := UserData{IncludeTorz: true, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "tb", Token: "synthetic"}}}}
			if tc.upstream {
				upstreamBase, err := url.Parse("https://upstream.invalid")
				require.NoError(t, err)
				ud.Upstreams = []UserDataUpstream{{URL: upstreamBase.String() + "/manifest.json", baseUrl: upstreamBase, ReconfigureStore: true}}
				previousHTTP := addon.HTTPClient
				t.Cleanup(func() { addon.HTTPClient = previousHTTP })
				addon.HTTPClient = &http.Client{Transport: selectedPathRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					require.Equal(t, "upstream.invalid", r.URL.Host)
					upstreamURL := upstreamBase.JoinPath("stremio/torz/synthetic/_/strem", sid, "tb", hash, "1", "E02.mkv")
					upstreamURL.RawQuery = url.Values{"filePath": []string{selectedPath}}.Encode()
					body, err := json.Marshal(stremio.StreamHandlerResponse{Streams: []stremio.Stream{{Name: "Synthetic", URL: upstreamURL.String(), InfoHash: hash, FileIndex: 1, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "E02.mkv"}}}})
					require.NoError(t, err)
					return &http.Response{StatusCode: http.StatusOK, Request: r, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})}
			}
			ud.SetEncoded("synthetic")
			req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://example.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("test")})
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			providerFiles := []store.MagnetFile{
				{Name: "E02.mkv", Path: otherPath, Idx: 42, Size: 100},
				{Name: "E02.mkv", Path: selectedPath, Idx: 99, Size: 100},
			}
			ud.GetStores()[0].Store = selectedPathStore{hash: hash, cached: tc.cached, files: []store.MagnetFile{providerFiles[1], providerFiles[0]}}
			result, err := ud.fetchStream(ctx, req, "series", sid)
			require.NoError(t, err)
			require.Len(t, result.Streams, 1)
			endpoint, err := url.Parse(result.Streams[0].URL)
			require.NoError(t, err)
			query := endpoint.Query()
			require.Equal(t, selectedPath, query.Get("filePath"))
			require.Equal(t, sid, query.Get("sid"))
			require.Equal(t, "TB", query.Get("s"))
			files := []store.File{&providerFiles[0], &providerFiles[1]}
			require.Nil(t, stremio_shared.MatchFileByName(files, "E02.mkv"))
			require.Nil(t, stremio_shared.MatchFileByIdx(files, 1, store.StoreCodeTorBox))
			played := stremio_shared.MatchFileByStremIdWithPath("Synthetic", files, sid, "", store.StoreCodeTorBox, query.Get("filePath"))
			require.Same(t, &providerFiles[1], played)
		})
	}
}

type selectedPathRoundTripFunc func(*http.Request) (*http.Response, error)

func (f selectedPathRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReconfigureSelectedPathConditions(t *testing.T) {
	const selectedPath = "/Season 1/Version A + # & ? % ü/E02.mkv"
	hash := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name                                         string
		reconfigure, haveHash, extractHash, havePath bool
		wantReconfigured                             bool
	}{
		{"hash field", true, true, false, true, true},
		{"extracted hash", true, false, true, true, true},
		{"no path", true, true, false, false, true},
		{"passthrough", false, true, false, true, false},
		{"unresolved hash", true, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, err := url.Parse("https://upstream.invalid/play/" + hash + "/1/E02.mkv")
			require.NoError(t, err)
			if tc.havePath {
				endpoint.RawQuery = url.Values{"filePath": []string{selectedPath}}.Encode()
			}
			upstream := &stremio.Stream{URL: endpoint.String(), FileIndex: 1, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "E02.mkv"}}
			if tc.haveHash {
				upstream.InfoHash = hash
			}
			transformer := StreamTransformer{}
			if tc.extractHash {
				transformer.Extractor = stremio_transformer.StreamExtractorBlob("url\n/(?P<hash>[a-f0-9]{40})/").MustParse()
			}
			result, err := transformer.Do(upstream, "series", tc.reconfigure)
			require.NoError(t, err)
			if tc.wantReconfigured {
				require.Empty(t, result.URL)
				require.Equal(t, hash, result.InfoHash)
				if tc.havePath {
					require.Equal(t, selectedPath, result.filePath)
				} else {
					require.Empty(t, result.filePath)
				}
			} else {
				require.Equal(t, endpoint.String(), result.URL)
				require.Empty(t, result.filePath)
			}
		})
	}
}
