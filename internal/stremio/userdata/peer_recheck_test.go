package stremio_userdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/debrider"
	"github.com/MunifTanjim/stremthru/store/easydebrid"
	"github.com/MunifTanjim/stremthru/store/premiumize"
	"github.com/stretchr/testify/require"
)

func TestPeerRecheckFailureSurvivesAdapterReuse(t *testing.T) {
	for _, name := range []store.StoreName{store.StoreNameDebrider, store.StoreNameEasyDebrid, store.StoreNamePremiumize} {
		for _, localOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/local=%t", name, localOnly), func(t *testing.T) {
				testutil.SetupDB(t)
				oldPeer, oldHasPeer, oldHasBuddy, oldLazy, oldRecheck := buddy.Peer, config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy, config.StoreRecheckMissingFileLists
				config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy, config.StoreRecheckMissingFileLists = true, false, false, true
				t.Cleanup(func() {
					buddy.Peer, config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy, config.StoreRecheckMissingFileLists = oldPeer, oldHasPeer, oldHasBuddy, oldLazy, oldRecheck
				})
				hash, missing := testutil.Hash(t), testutil.Hash(t)
				recorded := torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/movie.mkv"})
				buddy.Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					return testutil.JSONResponse(r, 200, map[string]any{"data": store.CheckMagnetData{RecheckError: store.ErrCheckMagnetRecheckFailed, Items: []store.CheckMagnetDataItem{
						{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv", Path: "/movie.mkv"}}},
						{Hash: missing, Status: store.MagnetStatusUnknown},
					}}}), nil
				})}})
				client := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					t.Errorf("unexpected direct lookup after peer failure: %s", r.URL.Path)
					return testutil.JSONResponse(r, 500, nil), nil
				})}
				var provider store.Store
				switch name {
				case store.StoreNameDebrider:
					provider = debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: client})
				case store.StoreNameEasyDebrid:
					provider = easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: client})
				case store.StoreNamePremiumize:
					provider = premiumize.NewStoreClient(&premiumize.StoreClientConfig{HTTPClient: client})
				}
				ud := UserDataStores{stores: []resolvedStore{{Store: provider, AuthToken: "fixture"}}}
				result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash, missing}, LocalOnly: localOnly}, logger.Scoped("test"))
				require.Eventually(t, recorded, 2*time.Second, 10*time.Millisecond)
				require.True(t, result.HasErr)
				require.ErrorIs(t, result.Err[0], store.ErrCheckMagnetRecheckFailed)
				require.Contains(t, result.ByHash, hash, "completed cached answers remain usable")
				require.NotContains(t, result.ByHash, missing)
				require.Len(t, result.HasErrByStoreCode, 1, "unresolved hashes must not be advertised as uncached")
			})
		}
	}
}

func TestPeerRecheckFailureStillChecksOmittedHashes(t *testing.T) {
	for _, name := range []store.StoreName{store.StoreNameDebrider, store.StoreNameEasyDebrid, store.StoreNamePremiumize} {
		for _, outcome := range []string{"cached", "lookup failure", "local only"} {
			t.Run(fmt.Sprintf("%s/%s", name, outcome), func(t *testing.T) {
				testutil.SetupDB(t)
				oldPeer, oldHasPeer, oldHasBuddy, oldLazy, oldRecheck := buddy.Peer, config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy, config.StoreRecheckMissingFileLists
				oldToken := config.PeerAuthToken
				config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy, config.StoreRecheckMissingFileLists = true, false, false, true
				config.PeerAuthToken = "fixture-token"
				t.Cleanup(func() {
					buddy.Peer, config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy, config.StoreRecheckMissingFileLists = oldPeer, oldHasPeer, oldHasBuddy, oldLazy, oldRecheck
					config.PeerAuthToken = oldToken
				})
				hashes := make([]string, 501)
				for i := range hashes {
					hashes[i] = testutil.Hash(t)
				}
				missing := hashes[500]
				peerRecorded := torrent_stream.ObserveFileRecordingForTesting(hashes[0], []string{"/peer.mkv"})
				providerRecorded := torrent_stream.ObserveFileRecordingForTesting(missing, []string{"/direct.mkv"})
				tracked := make(chan struct{}, 1)
				var peerCalls, directCalls atomic.Int32
				buddy.Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodPost {
						tracked <- struct{}{}
						return testutil.JSONResponse(r, 200, map[string]any{"data": map[string]any{}}), nil
					}
					peerCalls.Add(1)
					batch := r.URL.Query()["magnet"]
					if len(batch) == 1 {
						require.Equal(t, missing, batch[0])
						return testutil.JSONResponse(r, 503, map[string]any{"error": map[string]any{"message": "fixture chunk failure"}}), nil
					}
					require.Len(t, batch, 500)
					items := make([]store.CheckMagnetDataItem, len(batch))
					for i, hash := range batch {
						items[i] = store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached}
					}
					items[0].Files = []store.MagnetFile{{Name: "peer.mkv", Path: "/peer.mkv"}}
					return testutil.JSONResponse(r, 200, map[string]any{"data": store.CheckMagnetData{RecheckError: store.ErrCheckMagnetRecheckFailed, Items: items}}), nil
				})}})
				lookupFailure := errors.New("fixture direct lookup failure")
				client := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					directCalls.Add(1)
					if outcome == "local only" {
						t.Error("LocalOnly must not perform direct lookup")
					}
					if name == store.StoreNamePremiumize {
						require.NoError(t, r.ParseForm())
						if strings.HasSuffix(r.URL.Path, "/cache/check") {
							require.Equal(t, []string{missing}, r.Form["items[]"])
							if outcome == "lookup failure" {
								return nil, lookupFailure
							}
							return testutil.JSONResponse(r, 200, map[string]any{"status": "success", "response": []bool{true}, "filename": []string{"direct.mkv"}, "filesize": []int{100}}), nil
						}
						require.True(t, strings.HasSuffix(r.URL.Path, "/transfer/directdl"))
						require.Equal(t, "magnet:?xt=urn:btih:"+missing, r.PostForm.Get("src"))
						return testutil.JSONResponse(r, 200, map[string]any{"status": "success", "content": []map[string]any{{"path": "Folder/direct.mkv", "size": 100}}}), nil
					}
					var body struct {
						Data []string `json:"data"`
						URLs []string `json:"urls"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					links := body.Data
					if name == store.StoreNameEasyDebrid {
						links = body.URLs
					}
					require.Equal(t, []string{"magnet:?xt=urn:btih:" + missing}, links, "returned fileless answers must not be rechecked again")
					if outcome == "lookup failure" {
						return nil, lookupFailure
					}
					file := map[string]any{"name": "Folder/direct.mkv", "size": 100}
					if name == store.StoreNameEasyDebrid {
						file["name"], file["folder"] = "direct.mkv", "Folder"
					}
					return testutil.JSONResponse(r, 200, map[string]any{"result": []map[string]any{{"hash": missing, "cached": true, "files": []map[string]any{file}}}}), nil
				})}
				var provider store.Store
				switch name {
				case store.StoreNameDebrider:
					provider = debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: client})
				case store.StoreNameEasyDebrid:
					provider = easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: client})
				case store.StoreNamePremiumize:
					provider = premiumize.NewStoreClient(&premiumize.StoreClientConfig{HTTPClient: client})
				}
				params := &store.CheckMagnetParams{Magnets: hashes, LocalOnly: outcome == "local only"}
				params.APIKey = "fixture"
				beforeStats := checkStatsSnapshot(name)
				result, err := provider.CheckMagnet(params)
				afterStats := checkStatsSnapshot(name)
				require.Equal(t, beforeStats.Rechecks, afterStats.Rechecks, "an unrelated omitted hash is ordinary fallback, not another recheck")
				require.NoError(t, err)
				require.ErrorIs(t, result.RecheckError, store.ErrCheckMagnetRecheckFailed)
				require.EqualValues(t, 2, peerCalls.Load())
				require.Eventually(t, peerRecorded, 2*time.Second, 10*time.Millisecond)
				for i := 0; i < 500; i++ {
					require.Equal(t, hashes[i], result.Items[i].Hash)
					require.Equal(t, store.MagnetStatusCached, result.Items[i].Status)
				}
				if outcome == "local only" {
					require.Len(t, result.Items, 500)
					require.Zero(t, directCalls.Load())
					return
				}
				require.Equal(t, 501, len(result.Items), "omitted peer hashes must still reach direct fallback")
				wantCalls := 1
				if outcome == "cached" && name == store.StoreNamePremiumize {
					wantCalls = 2
				}
				require.EqualValues(t, wantCalls, directCalls.Load())
				if outcome == "lookup failure" {
					require.ErrorIs(t, result.RecheckError, lookupFailure)
					require.Equal(t, store.MagnetStatusUnknown, result.Items[500].Status)
				} else {
					require.Equal(t, store.MagnetStatusCached, result.Items[500].Status)
					require.Len(t, result.Items[500].Files, 1)
					select {
					case <-tracked:
					case <-time.After(2 * time.Second):
						t.Fatal("direct lookup tracking did not complete")
					}
					require.Eventually(t, providerRecorded, 2*time.Second, 10*time.Millisecond)
				}
			})
		}
	}
}
