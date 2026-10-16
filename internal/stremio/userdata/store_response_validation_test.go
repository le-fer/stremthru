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
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/debrider"
	"github.com/MunifTanjim/stremthru/store/easydebrid"
	"github.com/MunifTanjim/stremthru/store/offcloud"
	"github.com/MunifTanjim/stremthru/store/premiumize"
	"github.com/MunifTanjim/stremthru/store/torbox"
	"github.com/stretchr/testify/require"
)

func TestReturnedFileValidationIndependentOfCacheStatus(t *testing.T) {
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
	hash := strings.Repeat("ab", 20)
	statuses := []store.MagnetStatus{store.MagnetStatusCached, store.MagnetStatusUnknown, store.MagnetStatusQueued, store.MagnetStatusDownloading, store.MagnetStatusProcessing, store.MagnetStatusDownloaded, store.MagnetStatusUploading, store.MagnetStatusFailed, store.MagnetStatusInvalid}
	for _, name := range store.StoreNames {
		for _, status := range statuses {
			for _, allow := range []bool{false, true} {
				for _, tc := range []struct {
					name    string
					files   []store.MagnetFile
					invalid bool
				}{
					{"missing", nil, false},
					{"video", []store.MagnetFile{{Name: "movie.mkv"}}, false},
					{"nonvideo", []store.MagnetFile{{Name: "readme.txt"}}, true},
					{"blocked path", []store.MagnetFile{{Name: "movie.mkv", Path: "/setup.exe"}}, true},
					{"mixed", []store.MagnetFile{{Name: "movie.mkv"}, {Name: "setup.exe"}}, !allow},
				} {
					t.Run(fmt.Sprintf("%s/%s/allow=%t/%s", name, status, allow, tc.name), func(t *testing.T) {
						config.AllowVideoTorrentsWithDisallowedFiles = allow
						provider := &checkMagnetTestStore{name: name, item: store.CheckMagnetDataItem{Hash: hash, Status: status, Files: tc.files}}
						ud := UserDataStores{stores: []resolvedStore{{Store: provider}}}
						result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
						require.False(t, result.HasErr)
						_, invalid := result.InvalidByHash[hash]
						require.Equal(t, tc.invalid, invalid)
						require.Equal(t, status == store.MagnetStatusCached && !tc.invalid, result.ByHash[hash] != "")
					})
				}
			}
		}
	}
}

func TestOffcloudNoncachedFilesRejectKnownInvalidResults(t *testing.T) {
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	config.AllowVideoTorrentsWithDisallowedFiles = false
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
	hash := strings.Repeat("ab", 20)
	for _, filename := range []string{"setup.exe", "readme.txt"} {
		t.Run(filename, func(t *testing.T) {
			provider := offcloud.NewStoreClient(&offcloud.StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return testutil.JSONResponse(r, 200, []map[string]any{{"cached": false, "files": []map[string]any{{"filename": filename, "folder": []string{"Movie"}}}}}), nil
			})}})
			ud := UserDataStores{stores: []resolvedStore{{Store: provider, AuthToken: "fixture"}}}
			result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
			require.False(t, result.HasErr)
			require.Empty(t, result.ByHash)
			require.Contains(t, result.InvalidByHash, hash)
		})
	}
}

func TestCheckMagnetMergesDuplicateCanonicalFileEvidence(t *testing.T) {
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
	hash := strings.Repeat("ab", 20)
	for _, allow := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			for _, sibling := range []string{"readme.txt", "setup.exe", ""} {
				for _, fallback := range []bool{false, true} {
					t.Run(fmt.Sprintf("allow=%t/reverse=%t/sibling=%s/fallback=%t", allow, reverse, sibling, fallback), func(t *testing.T) {
						config.AllowVideoTorrentsWithDisallowedFiles = allow
						files := []store.MagnetFile{}
						if sibling != "" {
							files = append(files, store.MagnetFile{Name: sibling})
						}
						items := []store.CheckMagnetDataItem{
							{Hash: strings.ToUpper(hash), Status: store.MagnetStatusCached, Files: files},
							{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}},
						}
						if reverse {
							items[0], items[1] = items[1], items[0]
						}
						provider := &checkMagnetTestStore{name: store.StoreNameTorBox, items: items}
						ud := UserDataStores{stores: []resolvedStore{{Store: provider}}}
						if fallback {
							ud.stores = []resolvedStore{{Store: &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown}}}, {Store: provider}}
						}
						result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
						reject := sibling == "setup.exe" && !allow
						require.False(t, result.HasErr)
						_, invalid := result.InvalidByHash[hash]
						require.Equal(t, reject, invalid)
						if reject {
							require.Empty(t, result.ByHash)
						} else {
							require.Equal(t, "TB", result.ByHash[hash])
						}
					})
				}
			}
		}
	}
}

func TestRecheckAdaptersRetainDuplicateBuddyEvidence(t *testing.T) {
	originalBuddy, originalHasBuddy, originalHasPeer, originalMixed := buddy.Buddy, config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() {
		buddy.Buddy, config.HasBuddy, config.HasPeer, config.AllowVideoTorrentsWithDisallowedFiles = originalBuddy, originalHasBuddy, originalHasPeer, originalMixed
	})
	config.HasBuddy, config.HasPeer = true, false
	providers := []struct {
		name string
		new  func(*http.Client) store.Store
	}{
		{"torbox", func(c *http.Client) store.Store {
			return torbox.NewStoreClient(&torbox.StoreClientConfig{HTTPClient: c})
		}},
		{"debrider", func(c *http.Client) store.Store {
			return debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: c})
		}},
		{"easydebrid", func(c *http.Client) store.Store {
			return easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: c})
		}},
		{"premiumize", func(c *http.Client) store.Store {
			return premiumize.NewStoreClient(&premiumize.StoreClientConfig{HTTPClient: c})
		}},
	}
	for _, provider := range providers {
		for _, reverse := range []bool{false, true} {
			for _, allow := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/reverse=%t/allow=%t", provider.name, reverse, allow), func(t *testing.T) {
					testutil.SetupDB(t)
					hash := testutil.Hash(t)
					recorded := torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/movie.mkv", "/setup.exe"})
					config.AllowVideoTorrentsWithDisallowedFiles = allow
					items := []map[string]any{
						{"hash": strings.ToUpper(hash), "status": "cached", "files": []map[string]any{{"n": "setup.exe", "p": "/setup.exe"}}},
						{"hash": hash, "status": "cached", "files": []map[string]any{{"n": "movie.mkv", "p": "/movie.mkv"}}},
					}
					if reverse {
						items[0], items[1] = items[1], items[0]
					}
					buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
						return testutil.JSONResponse(r, 200, map[string]any{"data": map[string]any{"items": items}}), nil
					})}})
					directCalls := 0
					client := provider.new(&http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
						directCalls++
						return testutil.JSONResponse(r, 500, map[string]any{}), nil
					})})
					ud := UserDataStores{stores: []resolvedStore{{Store: client, AuthToken: "fixture"}}}
					result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
					require.Eventually(t, recorded, 2*time.Second, 10*time.Millisecond)
					require.Zero(t, directCalls, "a supplied merged list must not trigger a missing-list recheck")
					require.False(t, result.HasErr)
					_, invalid := result.InvalidByHash[hash]
					require.Equal(t, !allow, invalid)
					require.Equal(t, allow, result.ByHash[hash] != "")
				})
			}
		}
	}
}

func TestNoncachedEvidenceParticipatesInFallbackValidation(t *testing.T) {
	oldAllow, oldRecheck, oldCross := config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = oldAllow, oldRecheck, oldCross
	})
	config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = true, true
	hash := strings.Repeat("ab", 20)
	for _, allow := range []bool{false, true} {
		for _, tc := range []struct {
			name                      string
			firstStatus, secondStatus store.MagnetStatus
			firstFiles, secondFiles   []store.MagnetFile
			cached                    string
			invalid                   bool
		}{
			{"blocked miss then video", store.MagnetStatusUnknown, store.MagnetStatusCached, []store.MagnetFile{{Name: "setup.exe"}}, []store.MagnetFile{{Name: "movie.mkv"}}, "TB", !allow},
			{"provisional hit then blocked miss", store.MagnetStatusCached, store.MagnetStatusUnknown, nil, []store.MagnetFile{{Name: "setup.exe"}}, "", true},
			{"nonvideo miss then provisional hit", store.MagnetStatusUnknown, store.MagnetStatusCached, []store.MagnetFile{{Name: "readme.txt"}}, nil, "", true},
			{"video miss then provisional hit", store.MagnetStatusUnknown, store.MagnetStatusCached, []store.MagnetFile{{Name: "movie.mkv"}}, nil, "TB", false},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.name, allow), func(t *testing.T) {
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				first := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: tc.firstStatus, Files: tc.firstFiles}}
				second := &checkMagnetTestStore{name: store.StoreNameTorBox, item: store.CheckMagnetDataItem{Hash: hash, Status: tc.secondStatus, Files: tc.secondFiles}}
				ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: second}}}
				result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
				require.False(t, result.HasErr)
				expectedCalls := 1
				if tc.name == "blocked miss then video" && !allow {
					expectedCalls = 0
				}
				require.Equal(t, expectedCalls, second.calls)
				_, invalid := result.InvalidByHash[hash]
				require.Equal(t, tc.invalid, invalid)
				expected := tc.cached
				if tc.invalid {
					expected = ""
				}
				require.Equal(t, expected, result.ByHash[hash])
			})
		}
	}
}

func TestTorBoxDuplicateDirectResponsesRetainBlockedEvidence(t *testing.T) {
	oldMixed := config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles = oldMixed
	})
	config.AllowVideoTorrentsWithDisallowedFiles = false
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			testutil.SetupDB(t)
			waitForTracking := testutil.ObserveMagnetTracking(t)
			hash := testutil.Hash(t)
			recorded := torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/movie.mkv", "/setup.exe"})
			items := []map[string]any{
				{"hash": strings.ToUpper(hash), "name": "Movie", "files": []map[string]any{{"name": "Movie/setup.exe", "size": 1}}},
				{"hash": hash, "name": "Movie", "files": []map[string]any{{"name": "Movie/movie.mkv", "size": 100}}},
			}
			if reverse {
				items[0], items[1] = items[1], items[0]
			}
			provider := torbox.NewStoreClient(&torbox.StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return testutil.JSONResponse(r, 200, map[string]any{"success": true, "data": items}), nil
			})}})
			ud := UserDataStores{stores: []resolvedStore{{Store: provider, AuthToken: "fixture"}}}
			result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
			waitForTracking()
			require.Eventually(t, recorded, 2*time.Second, 10*time.Millisecond)
			require.False(t, result.HasErr)
			require.Empty(t, result.ByHash)
			require.Contains(t, result.InvalidByHash, hash)
		})
	}
}
