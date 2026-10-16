package stremio_wrap

import (
	"errors"
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
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/stretchr/testify/require"
)

func TestIncludeTorzKeepsDirectFallbackUntilAvailabilityIsKnown(t *testing.T) {
	for _, storedFiles := range []bool{false, true} {
		for _, outcome := range []string{"failure", "cached", "uncached", "rejected", "filtered fallback", "partial failure", "partial cached", "partial rejected", "partial filtered fallback", "partial healthy fallback", "partial cached-only healthy fallback"} {
			t.Run(fmt.Sprintf("files=%t/%s", storedFiles, outcome), func(t *testing.T) {
				testutil.SetupDB(t)
				hash := testutil.Hash(t)
				_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
				require.NoError(t, err)
				require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: hash}}))
				partial := strings.HasPrefix(outcome, "partial ")
				healthyHash := ""
				if partial {
					healthyHash = testutil.Hash(t)
					_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", healthyHash, "Movie.2024.720p", "tb", "Movie.2024.720p")
					require.NoError(t, err)
					require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{TId: "tt1234567", Hash: healthyHash}}))
				}
				if storedFiles {
					require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/movie.mkv", SId: "tt1234567"}}}, false))
				}
				oldPeer, oldBuddy, oldAddon, oldPull, oldLazy, oldMixed := config.HasPeer, config.HasBuddy, addon.HTTPClient, buddy.PullPeer, lazyPullTorz, config.AllowVideoTorrentsWithDisallowedFiles
				config.HasPeer, config.HasBuddy, buddy.PullPeer, lazyPullTorz, config.AllowVideoTorrentsWithDisallowedFiles = false, false, nil, false, false
				t.Cleanup(func() {
					config.HasPeer, config.HasBuddy, addon.HTTPClient, buddy.PullPeer, lazyPullTorz, config.AllowVideoTorrentsWithDisallowedFiles = oldPeer, oldBuddy, oldAddon, oldPull, oldLazy, oldMixed
				})
				addon.HTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					return testutil.JSONResponse(r, 200, map[string]any{"streams": []map[string]any{
						{"name": "direct", "infoHash": hash, "url": "https://fixture.invalid/direct"},
						{"name": "unrelated", "url": "https://fixture.invalid/unrelated"},
					}}), nil
				})}
				base, err := url.Parse("https://fixture.invalid")
				require.NoError(t, err)
				ud := UserData{IncludeTorz: true, Upstreams: []UserDataUpstream{{URL: base.String() + "/manifest.json", baseUrl: base}}, UserDataStores: userdata.UserDataStores{Stores: []userdata.Store{{Code: "ti", Token: "fixture"}}}}
				healthyFallback := strings.HasSuffix(outcome, "healthy fallback")
				if healthyFallback {
					ud.Stores = append(ud.Stores, userdata.Store{Code: "tb", Token: "fixture"})
				}
				ud.CachedOnly = outcome == "partial cached-only healthy fallback"
				if strings.HasSuffix(outcome, "filtered fallback") {
					ud.Filter = `Raw.Name != "direct"`
				}
				ud.SetEncoded("fixture")
				req := server.SetReqCtx(httptest.NewRequest(http.MethodGet, "http://fixture.invalid/stream", nil), &server.ReqCtx{Log: logger.Scoped("test")})
				ctx, err := ud.GetRequestContext(req)
				require.NoError(t, err)
				checks := 0
				ud.GetStores()[0].Store = torrin.NewStoreClient(&torrin.StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					checks++
					if outcome == "failure" || outcome == "filtered fallback" {
						return nil, errors.New("provider unavailable")
					}
					item := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached}
					if outcome == "uncached" || (partial && outcome != "partial cached" && outcome != "partial rejected") {
						item.Status = store.MagnetStatusUnknown
					} else if outcome == "rejected" || outcome == "partial rejected" {
						item.Files = []store.MagnetFile{{Name: "setup.exe", Path: "/setup.exe"}}
					}
					data := store.CheckMagnetData{Items: []store.CheckMagnetDataItem{item}}
					if partial {
						data.RecheckError = errors.New("provider unavailable")
						data.Items = append(data.Items, store.CheckMagnetDataItem{Hash: healthyHash, Status: store.MagnetStatusCached})
					}
					return testutil.JSONResponse(r, 200, map[string]any{"data": data}), nil
				})}})
				fallback := &uncachedFallbackTestStore{}
				if healthyFallback {
					ud.GetStores()[1].Store = fallback
				}
				result, err := ud.fetchStream(ctx, req, "movie", "tt1234567")
				require.NoError(t, err)
				require.Equal(t, 1, checks, "fallback must not add a second provider check")
				if healthyFallback {
					require.Equal(t, 1, fallback.checks, "existing cross-store fallback must still run once")
				}
				wantCount := 2
				if strings.HasSuffix(outcome, "rejected") || strings.HasSuffix(outcome, "filtered fallback") {
					wantCount = 1
				}
				if partial {
					wantCount++
				}
				require.Len(t, result.Streams, wantCount)
				var foundDirect, foundTorz, foundUnrelated, foundHealthy bool
				for _, stream := range result.Streams {
					foundDirect = foundDirect || stream.URL == "https://fixture.invalid/direct"
					foundTorz = foundTorz || strings.Contains(stream.URL, "/_/strem/"+hash+"/")
					foundHealthy = foundHealthy || (partial && strings.Contains(stream.URL, "/_/strem/"+healthyHash+"/"))
					foundUnrelated = foundUnrelated || stream.URL == "https://fixture.invalid/unrelated"
				}
				require.True(t, foundUnrelated)
				require.Equal(t, partial, foundHealthy, "partial failures must retain the other cached torrent")
				require.Equal(t, outcome == "failure" || outcome == "partial failure" || outcome == "partial cached-only healthy fallback", foundDirect)
				require.Equal(t, outcome == "cached" || outcome == "uncached" || outcome == "partial cached" || outcome == "partial healthy fallback", foundTorz, "usable Torz results must keep their duplicate priority")
			})
		}
	}
}

type uncachedFallbackTestStore struct {
	store.Store
	checks int
}

func (*uncachedFallbackTestStore) GetName() store.StoreName { return store.StoreNameTorBox }

func (s *uncachedFallbackTestStore) CheckMagnet(params *store.CheckMagnetParams) (*store.CheckMagnetData, error) {
	s.checks++
	data := &store.CheckMagnetData{}
	for _, hash := range params.Magnets {
		data.Items = append(data.Items, store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown})
	}
	return data, nil
}
