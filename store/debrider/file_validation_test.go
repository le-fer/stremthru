package debrider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
			_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameDebrider.Code()), hash)
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
		_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameDebrider.Code()), hash)
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

func TestInvalidLinkAvailabilityCardinalityIsReported(t *testing.T) {
	originalPeer, originalRecheck := config.HasPeer, config.StoreRecheckMissingFileLists
	config.HasPeer, config.StoreRecheckMissingFileLists = false, true
	t.Cleanup(func() {
		config.HasPeer, config.StoreRecheckMissingFileLists = originalPeer, originalRecheck
	})
	testutil.SetupDB(t)
	fileless, missing := testutil.Hash(t), testutil.Hash(t)
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameDebrider.Code()), fileless)
	require.NoError(t, err)
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.JSONResponse(r, 200, CheckLinkAvailabilityData{Result: []CheckLinkAvailabilityDataItem{{}, {}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{fileless}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.ErrorContains(t, result.RecheckError, "2 results for 1 magnets")
	require.Len(t, result.Items, 1)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)

	params.Magnets = []string{missing}
	result, err = client.CheckMagnet(params)
	require.Nil(t, result)
	require.ErrorContains(t, err, "2 results for 1 magnets")
}

func TestCheckMagnetMapsReturnedHashes(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sparse=%t", sparse), func(t *testing.T) {
			testutil.SetupDB(t)
			first, second := testutil.Hash(t), testutil.Hash(t)
			waitForTracking := testutil.ObserveMagnetTracking(t)
			filesRecorded := torrent_stream.ObserveFileRecordingForTesting(second, []string{"/second.mkv"})
			calls := 0
			client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				results := []CheckLinkAvailabilityDataItem{{Hash: strings.ToUpper(second), Cached: true, Files: []CheckLinkAvailabilityDataItemFile{{Name: "Folder/second.mkv"}}}}
				if !sparse {
					results = append(results, CheckLinkAvailabilityDataItem{Hash: first, Cached: true, Files: []CheckLinkAvailabilityDataItemFile{{Name: "Folder/first.mkv"}}})
				}
				return testutil.JSONResponse(r, 200, CheckLinkAvailabilityData{Result: results}), nil
			})}})
			params := &store.CheckMagnetParams{Magnets: []string{first, second}}
			params.APIKey = "fixture-token"
			result, err := client.CheckMagnet(params)
			require.NoError(t, err)
			waitForTracking()
			require.Eventually(t, filesRecorded, 5*time.Second, time.Millisecond)
			require.Equal(t, 1, calls)
			require.Len(t, result.Items, 2)
			require.Equal(t, first, result.Items[0].Hash)
			require.Equal(t, second, result.Items[1].Hash)
			require.Equal(t, "second.mkv", result.Items[1].Files[0].Name)
			if sparse {
				require.Equal(t, store.MagnetStatusUnknown, result.Items[0].Status)
				require.Empty(t, result.Items[0].Files)
			} else {
				require.Equal(t, "first.mkv", result.Items[0].Files[0].Name)
			}
			var count int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ? AND p = ?", first, "/second.mkv").Scan(&count))
			require.Zero(t, count, "files must never be persisted against a positional sibling")
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ? AND p = ?", second, "/second.mkv").Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestSparseLinkAvailabilityPreservesReusableAnswer(t *testing.T) {
	testutil.SetupDB(t)
	old := config.StoreRecheckMissingFileLists
	config.StoreRecheckMissingFileLists = true
	t.Cleanup(func() { config.StoreRecheckMissingFileLists = old })
	fileless, identified, missing := testutil.Hash(t), testutil.Hash(t), testutil.Hash(t)
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('dr',?,1,unixepoch())", fileless)
	require.NoError(t, err)
	waitForTracking := testutil.ObserveMagnetTracking(t)
	filesRecorded := torrent_stream.ObserveFileRecordingForTesting(identified, []string{"/movie.mkv"})
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.JSONResponse(r, 200, CheckLinkAvailabilityData{Result: []CheckLinkAvailabilityDataItem{{Hash: identified, Cached: true, Files: []CheckLinkAvailabilityDataItemFile{{Name: "Folder/movie.mkv"}}}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{fileless, identified, missing}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	waitForTracking()
	require.Eventually(t, filesRecorded, 5*time.Second, time.Millisecond)
	require.Nil(t, result.RecheckError, "identified sparse results do not establish a lookup failure")
	require.Len(t, result.Items, 3)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
	require.Empty(t, result.Items[0].Files)
	require.Equal(t, store.MagnetStatusCached, result.Items[1].Status)
	require.Len(t, result.Items[1].Files, 1)
	require.Equal(t, store.MagnetStatusUnknown, result.Items[2].Status)
	require.Empty(t, result.Items[2].Files)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM magnet_cache WHERE store = 'dr' AND hash = ?", missing).Scan(&count))
	require.Zero(t, count, "an omitted hash must not create a persisted miss")
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
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNameDebrider.Code()), hash)
	require.NoError(t, err)
	calls := 0
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/link/dlookup", r.URL.Path)
		var payload CheckLinkAvailabilityParams
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, []string{"magnet:?xt=urn:btih:" + hash}, payload.Data)
		return testutil.JSONResponse(r, 200, CheckLinkAvailabilityData{Result: []CheckLinkAvailabilityDataItem{{Hash: hash, Cached: true, Files: []CheckLinkAvailabilityDataItemFile{{Name: "Movie/movie.mkv", Size: 100}, {Name: "Movie/setup.exe", Size: 10}}}}}), nil
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
		{Idx: 0, Name: "movie.mkv", Path: "/movie.mkv", Size: 100, Source: string(store.StoreNameDebrider.Code())},
		{Idx: 1, Name: "setup.exe", Path: "/setup.exe", Size: 10, Source: string(store.StoreNameDebrider.Code())},
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
