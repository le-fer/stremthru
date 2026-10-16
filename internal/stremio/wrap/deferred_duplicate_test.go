package stremio_wrap

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/stretchr/testify/require"
)

func TestDeferredTorzPreservesUpstreamUntilResolved(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files       []string
		wantTorz    bool
		wantStreams int
		filter      string
	}{
		{"ambiguous list keeps upstream", []string{"opaque1.mkv", "opaque2.mkv"}, false, 1, ""},
		{"matched list keeps Torz priority", []string{"Series.S01E02.mkv"}, true, 1, ""},
		{"filtered candidate keeps upstream", []string{"video.mkv"}, false, 1, `File.Name != ""`},
		{"blocked list rejects both", []string{"Series.S01E02.mkv", "setup.exe"}, false, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture.SetupDB(t)
			hash := fixture.Hash(t)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/readme.txt", SId: "tt1234567:1:2"}}}, false))
			_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Series.1080p", "tb", "Series.1080p")
			require.NoError(t, err)
			require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: hash}}))
			oldPullPeer, oldLazy, oldAddon, oldPeer, oldAllow := buddy.PullPeer, lazyPullTorz, addon.HTTPClient, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles
			buddy.PullPeer, lazyPullTorz, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles = nil, false, false, false
			t.Cleanup(func() {
				buddy.PullPeer, lazyPullTorz, addon.HTTPClient, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles = oldPullPeer, oldLazy, oldAddon, oldPeer, oldAllow
			})
			addon.HTTPClient = &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return fixture.JSONResponse(r, 200, map[string]any{"streams": []map[string]any{{
					"name": "playable upstream 2160p", "infoHash": hash, "url": "https://fixture.invalid/playable",
					"behaviorHints": map[string]any{"filename": "Series.S01E02.mkv"},
				}}}), nil
			})}
			base, err := url.Parse("https://fixture.invalid")
			require.NoError(t, err)
			ud := UserData{IncludeTorz: true, Sort: "-resolution", Filter: tc.filter, Upstreams: []UserDataUpstream{{URL: base.String() + "/manifest.json", baseUrl: base}}, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "ti", Token: "fixture"}}}}
			ud.SetEncoded("fixture")
			req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("test")})
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			checks := 0
			ud.GetStores()[0].Store = torrin.NewStoreClient(&torrin.StoreClientConfig{HTTPClient: &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				checks++
				require.Equal(t, []string{hash}, r.URL.Query()["magnet"])
				files := []map[string]any{}
				for _, name := range tc.files {
					files = append(files, map[string]any{"name": name, "path": "/" + name})
				}
				return fixture.JSONResponse(r, 200, map[string]any{"data": map[string]any{"items": []map[string]any{{"hash": hash, "status": "cached", "files": files}}}}), nil
			})}})
			result, err := ud.fetchStream(ctx, req, "series", "tt1234567:1:2")
			require.NoError(t, err)
			require.Equal(t, 1, checks)
			require.Len(t, result.Streams, tc.wantStreams)
			if tc.wantStreams > 0 {
				if tc.wantTorz {
					require.Contains(t, result.Streams[0].URL, "/_/strem/")
				} else {
					require.Equal(t, "https://fixture.invalid/playable", result.Streams[0].URL)
				}
			}
		})
	}
}
