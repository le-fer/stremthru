package realdebrid

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestAddMagnetRejectsBeforeSelectingFiles(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, blocked := range []bool{false, true} {
			t.Run(fmt.Sprintf("blocked=%t/allow=%t", blocked, allow), func(t *testing.T) {
				testutil.SetupDB(t)
				previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
				hash := testutil.Hash(t)
				selected := 0
				deleted := 0
				infoCalls := 0
				files := []map[string]any{{"id": 1, "path": "/movie.mkv", "bytes": 100, "selected": 1}, {"id": 2, "path": "/subtitle.srt", "bytes": 1, "selected": 0}}
				if blocked {
					files[1]["path"] = "/setup.EXE"
				}
				c := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/torrents"):
						return testutil.JSONResponse(r, 200, []any{}), nil
					case strings.HasSuffix(r.URL.Path, "/addMagnet"):
						return testutil.JSONResponse(r, 200, map[string]any{"id": "fixture"}), nil
					case strings.Contains(r.URL.Path, "/info/"):
						infoCalls++
						status := "waiting_files_selection"
						if selected > 0 {
							status = "downloaded"
						}
						return testutil.JSONResponse(r, 200, map[string]any{"id": "fixture", "hash": hash, "filename": "Movie.2024.1080p", "status": status, "files": files, "links": []string{"https://fixture.invalid/movie"}}), nil
					case strings.Contains(r.URL.Path, "/selectFiles/"):
						selected++
						require.NoError(t, r.ParseForm())
						require.Equal(t, "1", r.Form.Get("files"))
						return testutil.JSONResponse(r, 204, nil), nil
					case strings.HasSuffix(r.URL.Path, "/delete/fixture"):
						deleted++
						return testutil.JSONResponse(r, 204, nil), nil
					default:
						t.Fatalf("unexpected request: %s", r.URL.Path)
						return nil, nil
					}
				})}})
				params := &store.AddMagnetParams{Magnet: hash}
				params.APIKey = "fixture-token"
				result, err := c.AddMagnet(params)
				if blocked && !allow {
					require.ErrorContains(t, err, "torrent contains disallowed files")
					require.Nil(t, result)
					require.Zero(t, selected)
					require.Equal(t, 1, deleted)
					require.Equal(t, 1, infoCalls)
					var stored, cacheEntries int
					require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&stored))
					require.Equal(t, 2, stored)
					require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM magnet_cache WHERE store = 'rd' AND hash = ?", hash).Scan(&cacheEntries))
					require.Zero(t, cacheEntries, "waiting for selection does not establish cache status")
				} else {
					require.NoError(t, err)
					require.Zero(t, deleted)
					require.Equal(t, 1, selected)
					require.Len(t, result.Files, 1)
					if blocked {
						stored, err := torrent_stream.GetFilesByHashes([]string{hash})
						require.NoError(t, err)
						require.Len(t, stored[hash], 2, "permissive selection must retain the unselected executable")
						require.True(t, stored[hash].HasDisallowedFile())
						var cacheEntries int
						require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM magnet_cache WHERE store = 'rd' AND hash = ?", hash).Scan(&cacheEntries))
						require.Zero(t, cacheEntries, "pre-selection evidence must not assert cached status")
						previousBuddy, previousPeer := config.HasBuddy, config.HasPeer
						config.HasBuddy, config.HasPeer = false, false
						t.Cleanup(func() { config.HasBuddy, config.HasPeer = previousBuddy, previousPeer })
						// Playback tracks only selected files. The broader evidence must survive.
						buddy.TrackMagnet(c, hash, result.Name, result.Size, result.Private, result.Files, "", false, params.APIKey)
						checked, err := c.CheckMagnet(&store.CheckMagnetParams{Ctx: params.Ctx, Magnets: []string{hash}, IsTrustedRequest: true})
						require.NoError(t, err)
						require.Len(t, checked.Items, 1)
						require.Len(t, checked.Items[0].Files, 2, "later cache/Buddy checks must retain the unselected executable")
					}
				}
			})
		}
	}
}

func TestIdHashCacheIgnoresUnsupportedHashes(t *testing.T) {
	c := NewStoreClient(&StoreClientConfig{})
	params := &store.ListMagnetsParams{}
	params.APIKey = "fixture-token"
	v2 := strings.Repeat("a", 64)

	c.addIdHashMapCache(params, "v2-id", v2)
	var hash string
	require.False(t, c.hashByIdCache.Get(c.getCacheKey(params, "v2-id"), &hash))
	ids := map[string]bool{}
	require.False(t, c.idsByHashCache.Get(c.getCacheKey(params, ""), &ids))

	v1Upper := strings.Repeat("AB", 20)
	c.addIdHashMapCache(params, "v1-id", v1Upper)
	require.True(t, c.hashByIdCache.Get(c.getCacheKey(params, "v1-id"), &hash))
	require.Equal(t, strings.ToLower(v1Upper), hash)
}

func TestVideoSelectionExcludesBlockedVideos(t *testing.T) {
	previous := util.FileExtDisallowed
	t.Cleanup(func() { util.FileExtDisallowed = previous })
	var err error
	util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
	require.NoError(t, err)
	torrent := &GetTorrentInfoData{Files: []GetTorrentInfoDataFile{
		{Id: 1, Path: "/movie.mkv"},
		{Id: 2, Path: "/Example.app/Contents/movie.mp4"},
		{Id: 3, Path: "/movie.mp4"},
		{Id: 4, Path: "/setup.exe"},
	}}
	require.Equal(t, []string{"2", "3"}, classifyTorrentFiles(torrent).videoFileIds)
}

func TestAddMagnetRejectsListsWithoutAllowedVideo(t *testing.T) {
	previousAllow, previousExtensions := config.AllowVideoTorrentsWithDisallowedFiles, util.FileExtDisallowed
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles, util.FileExtDisallowed = previousAllow, previousExtensions
	})
	var err error
	util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
	require.NoError(t, err)
	for _, allow := range []bool{false, true} {
		config.AllowVideoTorrentsWithDisallowedFiles = allow
		for _, tc := range []struct {
			path string
			err  string
		}{
			{path: "/movie.mkv", err: "torrent contains disallowed files"},
			{path: "/setup.exe", err: "torrent contains disallowed files"},
			{path: "/readme.txt", err: "torrent contains no allowed video files"},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.path, allow), func(t *testing.T) {
				testutil.SetupDB(t)
				hash := testutil.Hash(t)
				deleted := 0
				client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/torrents"):
						return testutil.JSONResponse(r, 200, []any{}), nil
					case strings.HasSuffix(r.URL.Path, "/addMagnet"):
						return testutil.JSONResponse(r, 200, map[string]any{"id": hash}), nil
					case strings.Contains(r.URL.Path, "/info/"):
						return testutil.JSONResponse(r, 200, map[string]any{"id": hash, "hash": hash, "status": "waiting_files_selection", "files": []map[string]any{{"id": 1, "path": tc.path, "bytes": 100}}}), nil
					case strings.HasSuffix(r.URL.Path, "/delete/"+hash):
						deleted++
						return testutil.JSONResponse(r, 204, nil), nil
					default:
						t.Fatalf("must reject before selecting files: %s", r.URL.Path)
						return nil, nil
					}
				})}})
				params := &store.AddMagnetParams{Magnet: hash}
				params.APIKey = "fixture-token"
				result, err := client.AddMagnet(params)
				require.ErrorContains(t, err, tc.err)
				require.Nil(t, result)
				require.Equal(t, 1, deleted)
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ? AND p = ?", hash, tc.path).Scan(&count))
				require.Equal(t, 1, count)
			})
		}
	}
}

func TestAddMagnetDownloadedRejectionSurvivesCleanupFailure(t *testing.T) {
	testutil.SetupDB(t)
	previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
	previousBuddy, previousPeer := config.HasBuddy, config.HasPeer
	originalBuddy := buddy.Buddy
	config.AllowVideoTorrentsWithDisallowedFiles = false
	config.HasBuddy, config.HasPeer = true, false
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles = previousAllow
		config.HasBuddy, config.HasPeer = previousBuddy, previousPeer
		buddy.Buddy = originalBuddy
	})

	tracked := make(chan string, 1)
	buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{
		BaseURL: "https://buddy.fixture.invalid",
		HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			tracked <- r.URL.Path
			return testutil.JSONResponse(r, http.StatusOK, map[string]any{"data": map[string]any{}}), nil
		})},
	})

	hash := testutil.Hash(t)
	deletedIds := []string{}
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/torrents"):
			return testutil.JSONResponse(r, 200, []any{}), nil
		case strings.HasSuffix(r.URL.Path, "/addMagnet"):
			return testutil.JSONResponse(r, 200, map[string]any{"id": "new-rejected"}), nil
		case strings.HasSuffix(r.URL.Path, "/info/new-rejected"):
			return testutil.JSONResponse(r, 200, map[string]any{
				"id": "new-rejected", "hash": hash, "filename": "Movie", "status": "downloaded",
				"files": []map[string]any{{"id": 1, "path": "/movie.mkv", "selected": 1}, {"id": 2, "path": "/setup.exe", "selected": 1}},
				"links": []string{"https://fixture.invalid/movie", "https://fixture.invalid/setup"},
			}), nil
		case strings.Contains(r.URL.Path, "/delete/"):
			deletedIds = append(deletedIds, strings.TrimPrefix(r.URL.Path, "/rest/1.0/torrents/delete/"))
			return testutil.JSONResponse(r, http.StatusInternalServerError, map[string]any{"error": "cleanup failed"}), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
			return nil, nil
		}
	})}})
	params := &store.AddMagnetParams{Magnet: hash}
	params.APIKey = "fixture-token"
	result, err := client.AddMagnet(params)
	require.ErrorContains(t, err, "torrent contains disallowed files", "cleanup failure must not replace the validation error")
	require.Nil(t, result)
	require.Equal(t, []string{"new-rejected"}, deletedIds, "cleanup must target only the torrent created by this request")

	var stored int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&stored))
	require.Equal(t, 2, stored)
	var cached bool
	require.NoError(t, db.QueryRow("SELECT is_cached FROM magnet_cache WHERE store = 'rd' AND hash = ?", hash).Scan(&cached))
	require.True(t, cached)
	select {
	case path := <-tracked:
		require.Equal(t, "/v0/store/magnet-cache/track", path)
	case <-time.After(time.Second):
		t.Fatal("downloaded rejected evidence was not published to Buddy")
	}
}

func TestAddMagnetRejectsExistingTorrentBeforeReplacement(t *testing.T) {
	testutil.SetupDB(t)
	previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
	previousBuddy, previousPeer := config.HasBuddy, config.HasPeer
	originalBuddy := buddy.Buddy
	config.AllowVideoTorrentsWithDisallowedFiles = false
	config.HasBuddy, config.HasPeer = true, false
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles = previousAllow
		config.HasBuddy, config.HasPeer = previousBuddy, previousPeer
		buddy.Buddy = originalBuddy
	})
	tracked := make(chan string, 1)
	buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{
		BaseURL: "https://buddy.fixture.invalid",
		HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			tracked <- r.URL.Path
			return testutil.JSONResponse(r, http.StatusOK, map[string]any{"data": map[string]any{}}), nil
		})},
	})
	hash := testutil.Hash(t)
	calls := []string{}
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/torrents"):
			return testutil.JSONResponse(r, 200, []map[string]any{{"id": hash, "hash": hash, "filename": "Movie"}}), nil
		case strings.Contains(r.URL.Path, "/info/"):
			return testutil.JSONResponse(r, 200, map[string]any{"id": hash, "hash": hash, "status": "downloaded", "files": []map[string]any{{"id": 1, "path": "/movie.mkv", "selected": 1}, {"id": 2, "path": "/setup.exe", "selected": 1}}, "links": []string{"https://fixture.invalid/movie", "https://fixture.invalid/setup"}}), nil
		default:
			t.Errorf("must reject before replacement: %s", r.URL.Path)
			return nil, fmt.Errorf("unexpected mutation")
		}
	})}})
	params := &store.AddMagnetParams{Magnet: hash}
	params.APIKey = "fixture-token"
	result, err := client.AddMagnet(params)
	require.ErrorContains(t, err, "torrent contains disallowed files")
	require.Nil(t, result)
	require.Len(t, calls, 2)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
	require.Equal(t, 2, count)
	var cached bool
	require.NoError(t, db.QueryRow("SELECT is_cached FROM magnet_cache WHERE store = 'rd' AND hash = ?", hash).Scan(&cached))
	require.True(t, cached, "downloaded provider status supports existing cache tracking")
	select {
	case path := <-tracked:
		require.Equal(t, "/v0/store/magnet-cache/track", path)
	case <-time.After(time.Second):
		t.Fatal("downloaded rejected evidence was not published to Buddy")
	}
}

func TestPermissiveReplacementRetainsDisallowedEvidence(t *testing.T) {
	testutil.SetupDB(t)
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	config.AllowVideoTorrentsWithDisallowedFiles = true
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
	hash := testutil.Hash(t)
	deleted, selected := false, false
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/torrents"):
			return testutil.JSONResponse(r, 200, []map[string]any{{"id": "old", "hash": hash, "filename": "Movie"}}), nil
		case strings.HasSuffix(r.URL.Path, "/info/old"):
			return testutil.JSONResponse(r, 200, map[string]any{"id": "old", "hash": hash, "filename": "Movie", "status": "downloaded", "files": []map[string]any{{"id": 1, "path": "/movie.mkv", "selected": 1}, {"id": 2, "path": "/setup.exe", "selected": 1}}}), nil
		case strings.HasSuffix(r.URL.Path, "/delete/old"):
			deleted = true
			return testutil.JSONResponse(r, 204, nil), nil
		case strings.HasSuffix(r.URL.Path, "/addMagnet"):
			require.True(t, deleted)
			return testutil.JSONResponse(r, 200, map[string]any{"id": "new"}), nil
		case strings.HasSuffix(r.URL.Path, "/info/new"):
			status := "waiting_files_selection"
			if selected {
				status = "downloaded"
			}
			return testutil.JSONResponse(r, 200, map[string]any{"id": "new", "hash": hash, "filename": "Movie", "status": status, "files": []map[string]any{{"id": 1, "path": "/movie.mkv", "selected": 1}}, "links": []string{"https://fixture.invalid/movie"}}), nil
		case strings.HasSuffix(r.URL.Path, "/selectFiles/new"):
			selected = true
			return testutil.JSONResponse(r, 204, nil), nil
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			return nil, fmt.Errorf("unexpected fixture request")
		}
	})}})
	params := &store.AddMagnetParams{Magnet: hash}
	params.APIKey = "fixture-token"
	result, err := client.AddMagnet(params)
	require.NoError(t, err)
	require.True(t, deleted)
	require.True(t, selected)
	require.Len(t, result.Files, 1)
	files, err := torrent_stream.GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	require.True(t, files[hash].HasDisallowedFile(), "replacement must not erase the original executable evidence")
}

func TestPermissiveReplacementCorrectsEqualCountWrongSelection(t *testing.T) {
	testutil.SetupDB(t)
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	config.AllowVideoTorrentsWithDisallowedFiles = true
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
	hash := testutil.Hash(t)
	deleted, selected := false, false
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/torrents"):
			return testutil.JSONResponse(r, 200, []map[string]any{{"id": "old", "hash": hash, "filename": "Movie"}}), nil
		case strings.HasSuffix(r.URL.Path, "/info/old"):
			return testutil.JSONResponse(r, 200, map[string]any{"id": "old", "hash": hash, "filename": "Movie", "status": "downloaded", "files": []map[string]any{{"id": 1, "path": "/movie.mkv", "selected": 0}, {"id": 2, "path": "/setup.exe", "selected": 1}}, "links": []string{"https://fixture.invalid/setup"}}), nil
		case strings.HasSuffix(r.URL.Path, "/delete/old"):
			deleted = true
			return testutil.JSONResponse(r, 204, nil), nil
		case strings.HasSuffix(r.URL.Path, "/addMagnet"):
			require.True(t, deleted)
			return testutil.JSONResponse(r, 200, map[string]any{"id": "new"}), nil
		case strings.HasSuffix(r.URL.Path, "/info/new"):
			status := "waiting_files_selection"
			if selected {
				status = "downloaded"
			}
			return testutil.JSONResponse(r, 200, map[string]any{"id": "new", "hash": hash, "filename": "Movie", "status": status, "files": []map[string]any{{"id": 1, "path": "/movie.mkv", "selected": 1}}, "links": []string{"https://fixture.invalid/movie"}}), nil
		case strings.HasSuffix(r.URL.Path, "/selectFiles/new"):
			require.NoError(t, r.ParseForm())
			require.Equal(t, "1", r.Form.Get("files"))
			selected = true
			return testutil.JSONResponse(r, 204, nil), nil
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			return nil, fmt.Errorf("unexpected fixture request")
		}
	})}})
	params := &store.AddMagnetParams{Magnet: hash}
	params.APIKey = "fixture-token"
	result, err := client.AddMagnet(params)
	require.NoError(t, err)
	require.True(t, deleted, "the existing torrent selected the wrong file ID despite an equal selection count")
	require.True(t, selected)
	require.Len(t, result.Files, 1)
	require.Equal(t, "movie.mkv", result.Files[0].Name)
	files, err := torrent_stream.GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	require.True(t, files[hash].HasDisallowedFile(), "replacement must retain the rejected executable as policy evidence")
}
