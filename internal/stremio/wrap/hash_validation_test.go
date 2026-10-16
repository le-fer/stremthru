package stremio_wrap

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MunifTanjim/go-ptt"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/stretchr/testify/require"
)

func TestDedupeStreamsNormalizesHashWithoutChangingMetadata(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	streams := []WrappedStream{
		{Stream: &stremio.Stream{Name: "Upper TITLE", InfoHash: strings.ToUpper(hash), BehaviorHints: &stremio.StreamBehaviorHints{Filename: "Movie.MKV"}}, r: &transformer.StreamExtractorResult{Hash: strings.ToUpper(hash)}},
		{Stream: &stremio.Stream{Name: "duplicate", InfoHash: hash}, r: &transformer.StreamExtractorResult{Hash: hash}},
		{Stream: &stremio.Stream{Name: "unknown", URL: "https://fixture.invalid/video"}},
	}
	result := dedupeStreams(streams)
	require.Len(t, result, 2)
	require.Equal(t, streams[0], result[0])
	require.Equal(t, "Upper TITLE", result[0].Name)
	require.Equal(t, "Movie.MKV", result[0].BehaviorHints.Filename)
	require.Equal(t, strings.ToUpper(hash), result[0].InfoHash)
	require.Equal(t, "unknown", result[1].Name)
}

func TestFetchStreamAppliesFreshHashRejectionToRetainedDuplicate(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, directFirst := range []bool{false, true} {
			for _, directOnly := range []bool{false, true} {
				for _, encoding := range []string{"hex", "upper", "base32"} {
					t.Run(fmt.Sprintf("allow=%t/direct-first=%t/direct-only=%t/encoding=%s", allow, directFirst, directOnly, encoding), func(t *testing.T) {
						testutil.SetupDB(t)
						previousAllow, previousPeer, previousAddon := config.AllowVideoTorrentsWithDisallowedFiles, config.HasPeer, addon.HTTPClient
						config.AllowVideoTorrentsWithDisallowedFiles, config.HasPeer = allow, false
						t.Cleanup(func() {
							config.AllowVideoTorrentsWithDisallowedFiles, config.HasPeer, addon.HTTPClient = previousAllow, previousPeer, previousAddon
						})
						hash := testutil.Hash(t)
						responseHash := hash
						if encoding == "upper" {
							responseHash = strings.ToUpper(hash)
						}
						if encoding == "base32" {
							bytes, err := hex.DecodeString(hash)
							require.NoError(t, err)
							responseHash = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes)
						}
						torrent := map[string]any{"name": "torrent", "infoHash": hash}
						direct := map[string]any{"name": "direct", "infoHash": strings.ToUpper(hash), "url": "https://fixture.invalid/direct"}
						streams := []map[string]any{torrent, direct}
						if directFirst {
							streams = []map[string]any{direct, torrent}
						}
						if directOnly {
							streams = []map[string]any{direct}
						}
						streams = append(streams, map[string]any{"name": "unrelated", "url": "https://fixture.invalid/unrelated"})
						addon.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
							return testutil.JSONResponse(r, 200, map[string]any{"streams": streams}), nil
						})}
						base, err := url.Parse("https://fixture.invalid")
						require.NoError(t, err)
						ud := UserData{Upstreams: []UserDataUpstream{{URL: base.String() + "/manifest.json", baseUrl: base}}, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "ti", Token: "fixture"}}}}
						ud.SetEncoded("fixture")
						req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("test")})
						ctx, err := ud.GetRequestContext(req)
						require.NoError(t, err)
						checks := 0
						ud.GetStores()[0].Store = torrin.NewStoreClient(&torrin.StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
							require.Equal(t, "/v0/store/magnets/check", r.URL.Path)
							require.Equal(t, []string{hash}, r.URL.Query()["magnet"], "check each normalized torrent hash once")
							checks++
							return testutil.JSONResponse(r, 200, map[string]any{"data": map[string]any{"items": []map[string]any{{"hash": responseHash, "status": "cached", "files": []map[string]any{{"name": "movie.mkv", "path": "/movie.mkv"}, {"name": "setup.exe", "path": "/setup.exe"}}}}}}), nil
						})}})
						result, err := ud.fetchStream(ctx, req, "movie", "fixture")
						require.NoError(t, err)
						if directOnly {
							require.Zero(t, checks)
						} else {
							require.Equal(t, 1, checks)
						}
						if !allow && !directOnly {
							require.Len(t, result.Streams, 1)
							require.Equal(t, "unrelated", result.Streams[0].Name)
						} else {
							require.Len(t, result.Streams, 2)
							if directFirst || directOnly {
								require.Equal(t, "https://fixture.invalid/direct", result.Streams[0].URL)
							}
						}
					})
				}
			}
		}
	}
}

func TestSelectStreamsPreservesIncludeTorzDuplicatePriority(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	for _, includeTorz := range []bool{false, true} {
		t.Run(fmt.Sprintf("include-torz=%t", includeTorz), func(t *testing.T) {
			streams := []WrappedStream{
				{Stream: &stremio.Stream{Name: "Torz", InfoHash: hash}, r: &transformer.StreamExtractorResult{Hash: hash, Result: &ptt.Result{Resolution: "1080p"}}},
				{Stream: &stremio.Stream{Name: "upstream", InfoHash: strings.ToUpper(hash)}, r: &transformer.StreamExtractorResult{Hash: hash, Result: &ptt.Result{Resolution: "2160p"}}},
			}
			selected := selectStreams(streams, nil, streamSelectionOptions{
				Sort:        "-resolution",
				SortEnabled: true,
				IncludeTorz: includeTorz,
			})
			require.Len(t, selected, 1)
			expected := "upstream"
			if includeTorz {
				expected = "Torz"
			}
			require.Equal(t, expected, selected[0].Name)
		})
	}
}
