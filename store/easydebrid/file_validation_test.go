package easydebrid

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestCheckMagnetRechecksMissingFileLists(t *testing.T) {
	originalPeer := config.HasPeer
	config.HasPeer = false
	t.Cleanup(func() { config.HasPeer = originalPeer })
	original := config.StoreRecheckMissingFileLists
	t.Cleanup(func() { config.StoreRecheckMissingFileLists = original })
	for _, tc := range []struct {
		name               string
		enabled, localOnly bool
		wantCalls          int
	}{
		{"enabled", true, false, 1},
		{"disabled", false, false, 0},
		{"local only", true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.SetupDB(t)
			hash := testutil.Hash(t)
			_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameEasyDebrid.Code()), hash)
			require.NoError(t, err)
			config.StoreRecheckMissingFileLists = tc.enabled
			calls := 0
			lookupError := errors.New("test provider lookup")
			client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return nil, lookupError
			})}})
			params := &store.CheckMagnetParams{Magnets: []string{hash}, LocalOnly: tc.localOnly}
			params.APIKey = "fixture-token"
			result, err := client.CheckMagnet(params)
			require.Equal(t, tc.wantCalls, calls)
			if tc.wantCalls > 0 {
				require.NoError(t, err)
				require.ErrorIs(t, result.RecheckError, lookupError)
				require.Len(t, result.Items, 1)
				require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
				require.Empty(t, result.Items[0].Files)
			} else {
				require.NoError(t, err)
				require.Len(t, result.Items, 1)
				require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
				require.Empty(t, result.Items[0].Files)
			}
		})
	}
}

func TestRecheckFailureRetainsOtherCachedResults(t *testing.T) {
	originalPeer := config.HasPeer
	config.HasPeer = false
	t.Cleanup(func() { config.HasPeer = originalPeer })
	testutil.SetupDB(t)
	old := config.StoreRecheckMissingFileLists
	config.StoreRecheckMissingFileLists = true
	t.Cleanup(func() { config.StoreRecheckMissingFileLists = old })
	valid, fileless, missing := testutil.Hash(t), testutil.Hash(t), testutil.Hash(t)
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: valid, File: torrent_stream.File{Name: "movie.mkv", Path: "/movie.mkv"}}}, false))
	for _, hash := range []string{valid, fileless} {
		_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameEasyDebrid.Code()), hash)
		require.NoError(t, err)
	}
	failure := errors.New("provider unavailable")
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, failure })}})
	params := &store.CheckMagnetParams{Magnets: []string{valid, fileless, missing}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.ErrorIs(t, result.RecheckError, failure)
	require.Len(t, result.Items, 3)
	require.Equal(t, valid, result.Items[0].Hash)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
	require.Len(t, result.Items[0].Files, 1)
	require.Equal(t, fileless, result.Items[1].Hash)
	require.Equal(t, store.MagnetStatusCached, result.Items[1].Status)
	require.Empty(t, result.Items[1].Files)
	require.Equal(t, missing, result.Items[2].Hash)
	require.Equal(t, store.MagnetStatusUnknown, result.Items[2].Status)
	params.Magnets = []string{missing}
	result, err = client.CheckMagnet(params)
	require.Nil(t, result)
	require.ErrorIs(t, err, failure, "ordinary lookup failure must remain an error")
}

func TestInvalidLinkDetailsCardinalityIsReported(t *testing.T) {
	originalPeer, originalRecheck := config.HasPeer, config.StoreRecheckMissingFileLists
	config.HasPeer, config.StoreRecheckMissingFileLists = false, true
	t.Cleanup(func() {
		config.HasPeer, config.StoreRecheckMissingFileLists = originalPeer, originalRecheck
	})
	testutil.SetupDB(t)
	fileless, missing := testutil.Hash(t), testutil.Hash(t)
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameEasyDebrid.Code()), fileless)
	require.NoError(t, err)
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.JSONResponse(r, 200, LookupLinkDetailsData{Result: []LookupLinkDetailsDataItem{{}, {}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{fileless}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.ErrorContains(t, result.RecheckError, "got 2 results for 1 magnets")
	require.Len(t, result.Items, 1)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)

	params.Magnets = []string{missing}
	result, err = client.CheckMagnet(params)
	require.Nil(t, result)
	require.ErrorContains(t, err, "got 2 results for 1 magnets")
}

func TestShortLinkDetailsResponseDoesNotAttributeFiles(t *testing.T) {
	testutil.SetupDB(t)
	oldPeer, oldBuddy, oldRecheck := config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists
	config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists = false, false, true
	t.Cleanup(func() {
		config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists = oldPeer, oldBuddy, oldRecheck
	})
	fileless, missing := testutil.Hash(t), testutil.Hash(t)
	calls := 0
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return testutil.JSONResponse(r, 200, LookupLinkDetailsData{Result: []LookupLinkDetailsDataItem{{Cached: true, Files: []LookupLinkDetailsDataItemFile{{Name: "second.mkv"}}}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{fileless, missing}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.Nil(t, result)
	require.ErrorContains(t, err, "got 1 results for 2 magnets")
	_, err = db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('ed',?,1,unixepoch())", fileless)
	require.NoError(t, err)
	result, err = client.CheckMagnet(params)
	require.NoError(t, err)
	require.ErrorContains(t, result.RecheckError, "got 1 results for 2 magnets")
	require.Len(t, result.Items, 2)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
	require.Equal(t, store.MagnetStatusUnknown, result.Items[1].Status)
	for _, item := range result.Items {
		require.Empty(t, item.Files)
	}
	require.Equal(t, 2, calls, "ambiguous responses must not trigger retries")
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h IN (?,?)", fileless, missing).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM magnet_cache WHERE store = 'ed' AND hash = ?", missing).Scan(&count))
	require.Zero(t, count, "unknown is not a persisted miss")
}

func TestSuccessfulRecheckReturnsAndRetainsFiles(t *testing.T) {
	testutil.SetupDB(t)
	previousRecheck := config.StoreRecheckMissingFileLists
	config.StoreRecheckMissingFileLists = true
	t.Cleanup(func() {
		config.StoreRecheckMissingFileLists = previousRecheck
	})
	hash := testutil.Hash(t)
	waitForTracking := testutil.ObserveMagnetTracking(t)
	filesRecorded := torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/movie.mkv", "/setup.exe"})
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameEasyDebrid.Code()), hash)
	require.NoError(t, err)
	calls := 0
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/link/lookupdetails", r.URL.Path)
		var payload LookupLinkDetailsParams
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, []string{"magnet:?xt=urn:btih:" + hash}, payload.URLs)
		return testutil.JSONResponse(r, 200, LookupLinkDetailsData{Result: []LookupLinkDetailsDataItem{{Cached: true, Files: []LookupLinkDetailsDataItemFile{{Name: "movie.mkv", Folder: "Movie", Size: 100}, {Name: "setup.exe", Folder: "Movie", Size: 10}}}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{hash}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	waitForTracking()
	require.NoError(t, err)
	require.Nil(t, result.RecheckError)
	require.Equal(t, 1, calls)
	require.Len(t, result.Items, 1)
	require.Equal(t, hash, result.Items[0].Hash)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
	require.Equal(t, []store.MagnetFile{
		{Idx: 0, Name: "movie.mkv", Path: "/movie.mkv", Size: 100, Source: string(store.StoreNameEasyDebrid.Code())},
		{Idx: 1, Name: "setup.exe", Path: "/setup.exe", Size: 10, Source: string(store.StoreNameEasyDebrid.Code())},
	}, result.Items[0].Files)
	// Wait for asynchronous tracking before restoring the database and settings.
	require.Eventually(t, filesRecorded, 5*time.Second, time.Millisecond)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
	require.Equal(t, 2, count, "blocked companions remain available as evidence")
	cached, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.Equal(t, 1, calls, "a retained nonempty list should not trigger another recheck")
	require.Len(t, cached.Items, 1)
	require.Equal(t, store.MagnetStatusCached, cached.Items[0].Status)
	require.Len(t, cached.Items[0].Files, 2)
}
