package stremio_wrap

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	userdata "github.com/MunifTanjim/stremthru/internal/stremio/userdata"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/stretchr/testify/require"
)

func TestFetchStreamPreservesDirectURLsAfterLookupFailure(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		direct, unrelated, torrent, rejected bool
		wantStreams, wantChecks              int
		wantError                            bool
	}{
		{"direct only", true, true, false, false, 2, 0, false},
		{"discarded torrent sibling", true, true, true, false, 2, 1, false},
		{"unrelated direct URL", false, true, true, false, 1, 1, false},
		{"torrent only", false, false, true, false, 0, 1, true},
		{"known rejection still applies", true, true, true, true, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.SetupDB(t)
			oldPeer, oldAddon, oldMixed := config.HasPeer, addon.HTTPClient, config.AllowVideoTorrentsWithDisallowedFiles
			config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles = false, false
			t.Cleanup(func() {
				config.HasPeer, addon.HTTPClient, config.AllowVideoTorrentsWithDisallowedFiles = oldPeer, oldAddon, oldMixed
			})
			hash := testutil.Hash(t)
			streams := []map[string]any{}
			if tc.direct {
				streams = append(streams, map[string]any{"name": "direct", "infoHash": hash, "url": "https://fixture.invalid/direct"})
			}
			if tc.unrelated {
				streams = append(streams, map[string]any{"name": "unrelated", "url": "https://fixture.invalid/unrelated"})
			}
			if tc.torrent {
				streams = append(streams, map[string]any{"name": "torrent", "infoHash": hash})
			}
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
				checks++
				if tc.rejected {
					return testutil.JSONResponse(r, 200, map[string]any{"data": store.CheckMagnetData{
						RecheckError: store.ErrCheckMagnetRecheckFailed,
						Items:        []store.CheckMagnetDataItem{{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "setup.exe", Path: "/setup.exe"}}}},
					}}), nil
				}
				return nil, errors.New("provider unavailable")
			})}})
			result, err := ud.fetchStream(ctx, req, "movie", "fixture")
			require.Equal(t, tc.wantChecks, checks)
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Streams, tc.wantStreams)
			for _, stream := range result.Streams {
				require.Contains(t, []string{"https://fixture.invalid/direct", "https://fixture.invalid/unrelated"}, stream.URL)
				if tc.rejected {
					require.Equal(t, "https://fixture.invalid/unrelated", stream.URL)
				}
			}
		})
	}
}
