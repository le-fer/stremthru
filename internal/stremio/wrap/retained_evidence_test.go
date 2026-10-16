package stremio_wrap

import (
	"fmt"
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
	userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	fixture "github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/stretchr/testify/require"
)

func TestCachedPlaybackRejectsRetainedBlockedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mixed     bool
		requested string
		blocked   bool
	}{
		{"strict companion", false, "movie.mkv", true},
		{"mixed video", true, "movie.mkv", false},
		{"mixed executable", true, "setup.exe", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture.SetupDB(t)
			old := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = tc.mixed
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = old })
			hash := fixture.Hash(t)
			req := httptest.NewRequest(http.MethodGet, "http://fixture.invalid/playback?sid=fixture", nil)
			req.SetPathValue("userData", util.Base64Encode(`{"stores":[{"c":"rd","t":"`+hash+`"}]}`))
			req.SetPathValue("magnetHash", hash)
			req.SetPathValue("fileIdx", "0")
			req.SetPathValue("fileName", tc.requested)
			req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("review"), ReqQuery: req.URL.Query()})
			ud, err := getUserData(req)
			require.NoError(t, err)
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			s := ud.GetStoreByCode("")
			code := s.Store.GetName().Code()
			key := strings.Join([]string{ctx.ClientIP, string(code), s.AuthToken, hash, "0", tc.requested, req.URL.Query().Encode()}, ":")
			stremLinkCache.Add(key, "https://fixture.invalid/cached-video")
			t.Cleanup(func() { stremLinkCache.Remove(key) })
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/movie.mkv"}}, {Hash: hash, File: torrent_stream.File{Path: "/setup.exe"}}}, false))
			res := httptest.NewRecorder()
			handleStrem(res, req)
			require.Equal(t, http.StatusFound, res.Code)
			if tc.blocked {
				require.NotEqual(t, "https://fixture.invalid/cached-video", res.Header().Get("Location"))
			} else {
				require.Equal(t, "https://fixture.invalid/cached-video", res.Header().Get("Location"))
			}
		})
	}
}
func TestStoredNonvideoAllowsFreshCachedVideo(t *testing.T) {
	for _, includeTorz := range []bool{false, true} {
		t.Run(fmt.Sprintf("includeTorz=%t", includeTorz), func(t *testing.T) {
			fixture.SetupDB(t)
			hash := fixture.Hash(t)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/readme.txt"}}}, false))
			oldAddon, oldPeer, oldPull, oldLazy := addon.HTTPClient, config.HasPeer, buddy.PullPeer, lazyPullTorz
			config.HasPeer, buddy.PullPeer, lazyPullTorz = false, nil, false
			t.Cleanup(func() {
				addon.HTTPClient, config.HasPeer, buddy.PullPeer, lazyPullTorz = oldAddon, oldPeer, oldPull, oldLazy
			})
			addon.HTTPClient = &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				streams := []map[string]any{{"name": "video", "infoHash": hash}}
				if includeTorz {
					streams = nil
				}
				return fixture.JSONResponse(r, 200, map[string]any{"streams": streams}), nil
			})}
			base, err := url.Parse("https://fixture.invalid")
			require.NoError(t, err)
			ud := UserData{Upstreams: []UserDataUpstream{{URL: base.String() + "/manifest.json", baseUrl: base}}, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "ti", Token: "fixture"}}}}
			ud.IncludeTorz = includeTorz
			_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
			require.NoError(t, err)
			require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: hash}}))
			ud.SetEncoded("fixture")
			req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("review")})
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			checks := 0
			ud.GetStores()[0].Store = torrin.NewStoreClient(&torrin.StoreClientConfig{HTTPClient: &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				checks++
				return fixture.JSONResponse(r, 200, map[string]any{"data": map[string]any{"items": []map[string]any{{"hash": hash, "status": "cached", "files": []map[string]any{{"name": "movie.mkv", "path": "/movie.mkv"}}}}}}), nil
			})}})
			streamId := "fixture"
			if includeTorz {
				streamId = "tt1234567"
			}
			result, err := ud.fetchStream(ctx, req, "movie", streamId)
			require.NoError(t, err)
			require.Len(t, result.Streams, 1)
			require.Equal(t, 1, checks)
		})
	}
}

func TestDeferredTorzUsesFinalMetadata(t *testing.T) {
	for _, filterCase := range []struct {
		expression string
		want       int
		sort       string
		fileName   string
	}{
		{`File.Name == ""`, 0, "", "Series.S01E02.mkv"},
		{`File.Name == "Series.S01E02.mkv"`, 1, "", "Series.S01E02.mkv"},
		{`true`, 2, "-size", "Series.S01E02.mkv"},
		{`true`, 1, "", "video.mkv"},
		{`true`, 0, "", "Series.S01E03.mkv"},
	} {
		includeTorz := true
		t.Run(filterCase.expression+"/"+filterCase.fileName, func(t *testing.T) {
			fixture.SetupDB(t)
			hash := fixture.Hash(t)
			secondHash := fixture.Hash(t)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/readme.txt", SId: "tt1234567:1:2"}}}, false))
			oldPullPeer, oldLazy := buddy.PullPeer, lazyPullTorz
			buddy.PullPeer, lazyPullTorz = nil, false
			t.Cleanup(func() { buddy.PullPeer, lazyPullTorz = oldPullPeer, oldLazy })
			oldAddon, oldPeer := addon.HTTPClient, config.HasPeer
			config.HasPeer = false
			t.Cleanup(func() { addon.HTTPClient, config.HasPeer = oldAddon, oldPeer })
			addon.HTTPClient = &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				streams := []map[string]any{{"name": "video", "infoHash": hash}}
				if includeTorz {
					streams = nil
				}
				return fixture.JSONResponse(r, 200, map[string]any{"streams": streams}), nil
			})}
			base, err := url.Parse("https://fixture.invalid")
			require.NoError(t, err)
			ud := UserData{Upstreams: []UserDataUpstream{{URL: base.String() + "/manifest.json", baseUrl: base}}, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "ti", Token: "fixture"}}}}
			ud.IncludeTorz = includeTorz
			ud.Filter = filterCase.expression
			ud.Sort = filterCase.sort
			checkedName := filterCase.fileName
			_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
			require.NoError(t, err)
			require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: hash}}))
			if filterCase.sort != "" {
				require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: secondHash, File: torrent_stream.File{Path: "/readme.txt", SId: "tt1234567:1:2"}}}, false))
				_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", secondHash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
				require.NoError(t, err)
				require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: secondHash}}))
			}
			ud.SetEncoded("fixture")
			req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("review")})
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			checks := 0
			ud.GetStores()[0].Store = torrin.NewStoreClient(&torrin.StoreClientConfig{HTTPClient: &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				checks++
				items := []map[string]any{}
				for i, h := range r.URL.Query()["magnet"] {
					items = append(items, map[string]any{"hash": h, "status": "cached", "files": []map[string]any{{"name": checkedName, "path": "/" + checkedName, "index": 9, "size": 1000 * (i + 1)}}})
				}
				expectedHashes := 1
				if filterCase.sort != "" {
					expectedHashes = 2
				}
				require.Len(t, items, expectedHashes)
				return fixture.JSONResponse(r, 200, map[string]any{"data": map[string]any{"items": items}}), nil
			})}})
			result, err := ud.fetchStream(ctx, req, "series", "tt1234567:1:2")
			require.NoError(t, err)
			require.Len(t, result.Streams, filterCase.want)
			require.Equal(t, 1, checks)
			if filterCase.want > 0 {
				if filterCase.fileName == "video.mkv" {
					require.Empty(t, result.Streams[0].BehaviorHints.Filename, "an opaque name validates the list without claiming an episode match")
				} else {
					require.Equal(t, filterCase.fileName, result.Streams[0].BehaviorHints.Filename)
				}
				if filterCase.sort != "" {
					require.EqualValues(t, 2000, result.Streams[0].BehaviorHints.VideoSize)
					require.EqualValues(t, 1000, result.Streams[1].BehaviorHints.VideoSize)
				}
			}
			if filterCase.fileName == "video.mkv" {
				require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/video.mkv", Idx: 7}}}, false))
				for i, fresh := range []struct {
					name string
					want int
				}{
					{"video.mkv", 1},
					{"Series.S01E02.mkv", 1},
					{"Series.S01E03.mkv", 0},
					{"Series.S02E02.mkv", 0},
				} {
					checkedName = fresh.name
					next, err := ud.fetchStream(ctx, req, "series", "tt1234567:1:2")
					require.NoError(t, err)
					require.Len(t, next.Streams, fresh.want, "checked evidence must resolve the stored opaque candidate: %s", fresh.name)
					if fresh.want > 0 {
						if fresh.name == "video.mkv" {
							require.Empty(t, next.Streams[0].BehaviorHints.Filename)
						} else {
							require.Equal(t, fresh.name, next.Streams[0].BehaviorHints.Filename)
							require.Contains(t, next.Streams[0].URL, "/9/"+fresh.name)
						}
					}
					require.Equal(t, i+2, checks, "each request must keep its existing single batched availability check")
				}
			}
		})
	}
}
