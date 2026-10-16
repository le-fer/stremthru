package stremio_userdata

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/magnet_cache"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/easydebrid"
	"github.com/stretchr/testify/require"
)

func TestCheckMagnetReusesStoredEvidenceWithoutPromotingCacheStatus(t *testing.T) {
	testutil.SetupDB(t)
	previousBuddy, previousPeer, previousAllow := config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles
	config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles = false, false, false
	t.Cleanup(func() {
		config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles = previousBuddy, previousPeer, previousAllow
	})

	hash := testutil.Hash(t)
	provider := &buddyEvidenceTestStore{Store: easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{})}
	magnet_cache.Touch(provider.GetName().Code(), hash, torrent_stream.Files{{Path: "/setup.exe"}}, false, false)
	ud := UserDataStores{stores: []resolvedStore{{Store: provider, AuthToken: "fixture"}}}
	result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))

	require.False(t, result.HasErr)
	require.Contains(t, result.InvalidByHash, hash)
	require.Len(t, provider.files, 1)
	require.Equal(t, "/setup.exe", provider.files[0].Path)
	rows, err := magnet_cache.GetByHashes(provider.GetName().Code(), []string{hash}, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].IsCached)
}

type buddyEvidenceTestStore struct {
	store.Store
	files []store.MagnetFile
}

func (s *buddyEvidenceTestStore) CheckMagnet(params *store.CheckMagnetParams) (*store.CheckMagnetData, error) {
	data, err := s.Store.CheckMagnet(params)
	if err == nil && len(data.Items) > 0 {
		s.files = data.Items[0].Files
	}
	return data, err
}

func TestCheckMagnetPreservesBuddyFileEvidenceBeforeValidation(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, blockedFirst := range []bool{false, true} {
			for _, source := range []string{"buddy", "peer"} {
				for _, status := range []store.MagnetStatus{store.MagnetStatusCached, store.MagnetStatusUnknown} {
					t.Run(fmt.Sprintf("allow=%t/blocked-first=%t/source=%s/status=%s", allow, blockedFirst, source, status), func(t *testing.T) {
						testutil.SetupDB(t)
						previousBuddy, previousHasBuddy, previousPeer, previousAllow, previousRecheck := buddy.Buddy, config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists
						oldPeer, oldLazy := buddy.Peer, config.PeerFlag.Lazy
						config.PeerFlag.Lazy = false
						config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists = source == "buddy", source == "peer", allow, false
						t.Cleanup(func() {
							buddy.Peer, config.PeerFlag.Lazy = oldPeer, oldLazy
							buddy.Buddy, config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists = previousBuddy, previousHasBuddy, previousPeer, previousAllow, previousRecheck
						})
						hash := testutil.Hash(t)
						recorded := torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/movie.mkv"})
						video := map[string]any{"n": "movie.mkv", "p": "/movie.mkv"}
						blocked := map[string]any{"n": "setup.exe", "p": "/movie.mkv"}
						files := []map[string]any{video, blocked, video}
						if blockedFirst {
							files = []map[string]any{blocked, video, blocked}
						}
						httpClient := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
							expectedPath := "/v0/store/magnet-cache/check"
							if source == "peer" {
								expectedPath = "/v0/store/magnets/check"
							}
							require.Equal(t, expectedPath, r.URL.Path)
							return testutil.JSONResponse(r, 200, map[string]any{"data": map[string]any{"items": []map[string]any{{"hash": strings.ToUpper(hash), "status": status, "files": files}}}}), nil
						})}
						if source == "buddy" {
							buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: httpClient})
						} else {
							peerFiles := make([]map[string]any, 0, len(files))
							for _, f := range files {
								peerFiles = append(peerFiles, map[string]any{"name": f["n"], "path": f["p"]})
							}
							files = peerFiles
							buddy.Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: httpClient})
						}
						provider := &buddyEvidenceTestStore{Store: easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{})}
						ud := UserDataStores{stores: []resolvedStore{{Store: provider, AuthToken: "fixture"}}}
						result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
						require.Eventually(t, recorded, 2*time.Second, 10*time.Millisecond)
						require.False(t, result.HasErr)
						require.Len(t, provider.files, 2, "keep conflicting names for validation and remove exact duplicates")
						_, accepted := result.ByHash[hash]
						require.Equal(t, allow && status == store.MagnetStatusCached, accepted)
						if allow {
							require.NotContains(t, result.InvalidByHash, hash)
						} else {
							require.Contains(t, result.InvalidByHash, hash)
						}
						stored, err := torrent_stream.GetFilesByHashes([]string{hash})
						require.NoError(t, err)
						require.Len(t, stored[hash], 1, "conflicting blocked name is still skipped during recording")
						require.Equal(t, "movie.mkv", stored[hash][0].Name)
					})
				}
			}
		}
	}
}
