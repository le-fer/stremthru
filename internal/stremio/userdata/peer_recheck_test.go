package stremio_userdata

import (
	"fmt"
	"net/http"
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
