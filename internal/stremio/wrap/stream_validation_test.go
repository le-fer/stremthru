package stremio_wrap

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	"github.com/MunifTanjim/stremthru/internal/shared"
	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	stremio_userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/stretchr/testify/require"
)

func TestFetchStreamSuppressesInvalidUncachedFallback(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = allow
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
			testutil.SetupDB(t)
			blocked, allowed, unknown, blockedWithoutCache := testutil.Hash(t), testutil.Hash(t), testutil.Hash(t), testutil.Hash(t)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
				{Hash: blocked, File: torrent_stream.File{Path: "/movie.mkv"}},
				{Hash: blocked, File: torrent_stream.File{Path: "/setup.exe"}},
				{Hash: blockedWithoutCache, File: torrent_stream.File{Path: "/movie.mkv"}},
				{Hash: blockedWithoutCache, File: torrent_stream.File{Path: "/setup.exe"}},
				{Hash: allowed, File: torrent_stream.File{Path: "/movie.mkv"}},
			}, false))
			for _, hash := range []string{blocked, allowed} {
				_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('rd',?,1,unixepoch())", hash)
				require.NoError(t, err)
			}
			api := shared.GetStore("realdebrid").(*realdebrid.StoreClient).GetClient()
			oldAPI, oldAddon, oldPeer := api.HTTPClient, addon.HTTPClient, config.HasPeer
			config.HasPeer = false
			t.Cleanup(func() { api.HTTPClient, addon.HTTPClient, config.HasPeer = oldAPI, oldAddon, oldPeer })
			api.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Contains(t, r.URL.Path, "/user")
				return testutil.JSONResponse(r, 200, map[string]any{"id": 1, "premium": 3600}), nil
			})}
			addon.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return testutil.JSONResponse(r, 200, map[string]any{"streams": []map[string]any{
					{"name": "blocked", "infoHash": blocked}, {"name": "allowed", "infoHash": allowed}, {"name": "unknown", "infoHash": unknown},
					{"name": "blocked without cache", "infoHash": blockedWithoutCache},
				}}), nil
			})}
			baseURL, err := url.Parse("https://fixture.invalid")
			require.NoError(t, err)
			ud := UserData{Upstreams: []UserDataUpstream{{URL: baseURL.String() + "/manifest.json", baseUrl: baseURL}}, UserDataStores: stremio_userdata.UserDataStores{Stores: []stremio_userdata.Store{{Code: "rd", Token: unknown}}}}
			ud.SetEncoded("fixture")
			req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("test")})
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			result, err := ud.fetchStream(ctx, req, "movie", "fixture")
			require.NoError(t, err)
			want := 2
			if allow {
				want = 4
			}
			require.Len(t, result.Streams, want)
			for _, stream := range result.Streams {
				if !allow {
					require.NotContains(t, stream.URL, blocked)
					require.NotContains(t, stream.URL, blockedWithoutCache)
				}
			}
			var hasAllowed, hasUnknown bool
			for _, stream := range result.Streams {
				hasAllowed = hasAllowed || strings.Contains(stream.URL, allowed)
				hasUnknown = hasUnknown || strings.Contains(stream.URL, unknown)
			}
			require.True(t, hasAllowed)
			require.True(t, hasUnknown)
		})
	}
}

func TestFetchStreamExcludesExplicitBlockedFilename(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = allow
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
			testutil.SetupDB(t)
			blocked, allowed, unknown := testutil.Hash(t), testutil.Hash(t), testutil.Hash(t)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
				{Hash: blocked, File: torrent_stream.File{Path: "/movie.mkv"}},

				{Hash: allowed, File: torrent_stream.File{Path: "/movie.mkv"}},
			}, false))
			for _, hash := range []string{blocked, allowed} {
				_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('rd',?,1,unixepoch())", hash)
				require.NoError(t, err)
			}
			api := shared.GetStore("realdebrid").(*realdebrid.StoreClient).GetClient()
			oldAPI, oldAddon, oldPeer := api.HTTPClient, addon.HTTPClient, config.HasPeer
			config.HasPeer = false
			t.Cleanup(func() { api.HTTPClient, addon.HTTPClient, config.HasPeer = oldAPI, oldAddon, oldPeer })
			api.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Contains(t, r.URL.Path, "/user")
				return testutil.JSONResponse(r, 200, map[string]any{"id": 1, "premium": 3600}), nil
			})}
			addon.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return testutil.JSONResponse(r, 200, map[string]any{"streams": []map[string]any{
					{"name": "blocked-video", "infoHash": blocked, "behaviorHints": map[string]any{"filename": "movie.mkv"}},
					{"name": "blocked-executable", "infoHash": blocked, "behaviorHints": map[string]any{"filename": "setup.exe"}},
					{"name": "allowed", "infoHash": allowed}, {"name": "unknown", "infoHash": unknown},
				}}), nil
			})}
			baseURL, err := url.Parse("https://fixture.invalid")
			require.NoError(t, err)
			ud := UserData{Upstreams: []UserDataUpstream{{URL: baseURL.String() + "/manifest.json", baseUrl: baseURL}}, UserDataStores: stremio_userdata.UserDataStores{Stores: []stremio_userdata.Store{{Code: "rd", Token: unknown}}}}
			ud.SetEncoded("fixture")
			req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("test")})
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			result, err := ud.fetchStream(ctx, req, "movie", "fixture")
			require.NoError(t, err)
			want := 2
			if allow {
				want = 3
			}
			require.Len(t, result.Streams, want)
			for _, stream := range result.Streams {
				require.NotContains(t, stream.Name, "blocked-executable")
				if !allow {
					require.NotContains(t, stream.URL, blocked)
				}
			}
			var hasAllowed, hasUnknown bool
			for _, stream := range result.Streams {
				hasAllowed = hasAllowed || strings.Contains(stream.URL, allowed)
				hasUnknown = hasUnknown || strings.Contains(stream.URL, unknown)
			}
			require.True(t, hasAllowed)
			require.True(t, hasUnknown)
		})
	}
}

func TestBlockedFileFilteringPrecedesDeduplication(t *testing.T) {
	hash := strings.Repeat("a", 40)
	streams := []WrappedStream{
		{Stream: &stremio.Stream{InfoHash: hash, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "setup.exe"}}, r: &stremio_transformer.StreamExtractorResult{Hash: hash}},
		{Stream: &stremio.Stream{Name: "video", InfoHash: hash, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "movie.mkv"}}, r: &stremio_transformer.StreamExtractorResult{Hash: hash}},
		{Stream: &stremio.Stream{Name: "unknown", URL: "https://fixture.invalid/video"}},
	}
	strict := dedupeStreams(filterDisallowedFileStreams(streams, false))
	require.Len(t, strict, 1)
	require.Equal(t, "unknown", strict[0].Name)
	permissive := dedupeStreams(filterDisallowedFileStreams(streams, true))
	require.Len(t, permissive, 2)
	require.Equal(t, "video", permissive[0].Name)
	require.Equal(t, "movie.mkv", permissive[0].BehaviorHints.Filename)
}

func TestBlockedHashlessStreamDoesNotRemoveUnrelatedURLs(t *testing.T) {
	for _, hash := range []string{"", "invalid", "magnet:?xt=urn:btih:"} {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("hash=%q/allow=%t", hash, allow), func(t *testing.T) {
				streams := []WrappedStream{
					{Stream: &stremio.Stream{Name: "blocked", URL: "https://fixture.invalid/setup", InfoHash: hash, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "setup.exe"}}},
					{Stream: &stremio.Stream{Name: "video", URL: "https://fixture.invalid/movie", BehaviorHints: &stremio.StreamBehaviorHints{Filename: "movie.mkv"}}},
					{Stream: &stremio.Stream{Name: "unknown", URL: "https://fixture.invalid/unknown"}},
				}
				result := filterDisallowedFileStreams(streams, allow)
				require.Len(t, result, 2)
				require.Equal(t, "video", result[0].Name)
				require.Equal(t, "unknown", result[1].Name)
			})
		}
	}
}

func TestBlockedFileFilteringUsesExtractedHashForDirectURLs(t *testing.T) {
	hash := strings.Repeat("a", 40)
	streams := []WrappedStream{
		{Stream: &stremio.Stream{URL: "https://fixture.invalid/setup", BehaviorHints: &stremio.StreamBehaviorHints{Filename: "setup.exe"}}, r: &stremio_transformer.StreamExtractorResult{Hash: hash}},
		{Stream: &stremio.Stream{URL: "https://fixture.invalid/movie", BehaviorHints: &stremio.StreamBehaviorHints{Filename: "movie.mkv"}}, r: &stremio_transformer.StreamExtractorResult{Hash: strings.ToUpper(hash)}},
		{Stream: &stremio.Stream{URL: "https://fixture.invalid/unrelated"}},
	}
	strict := filterDisallowedFileStreams(streams, false)
	require.Len(t, strict, 1)
	require.Equal(t, "https://fixture.invalid/unrelated", strict[0].URL)
	permissive := filterDisallowedFileStreams(streams, true)
	require.Len(t, permissive, 2)
	require.Equal(t, "https://fixture.invalid/movie", permissive[0].URL)
}

func TestStoredFileFilteringWithoutProviderCache(t *testing.T) {
	testutil.SetupDB(t)
	mixed, noVideo := testutil.Hash(t), testutil.Hash(t)
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
		{Hash: mixed, File: torrent_stream.File{Path: "/movie.mkv"}},
		{Hash: mixed, File: torrent_stream.File{Path: "/setup.exe"}},
		{Hash: noVideo, File: torrent_stream.File{Path: "/readme.txt"}},
	}, false))
	streams := []WrappedStream{
		{Stream: &stremio.Stream{URL: "https://fixture.invalid/mixed"}, r: &stremio_transformer.StreamExtractorResult{Hash: mixed}},
		{Stream: &stremio.Stream{InfoHash: noVideo}},
		{Stream: &stremio.Stream{URL: "https://fixture.invalid/unrelated"}},
	}
	strict, err := filterStoredFileStreams(streams, storedFileFilterOptions{Mode: storedFileFilterViability})
	require.NoError(t, err)
	require.Len(t, strict, 1)
	require.Equal(t, "https://fixture.invalid/unrelated", strict[0].URL)
	permissive, err := filterStoredFileStreams(streams, storedFileFilterOptions{AllowMixed: true, Mode: storedFileFilterViability})
	require.NoError(t, err)
	require.Len(t, permissive, 2)
	require.Equal(t, "https://fixture.invalid/mixed", permissive[0].URL)
}
