package premiumize

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestConcurrentBatchFailures(t *testing.T) {
	for _, rechecking := range []bool{false, true} {
		t.Run(fmt.Sprintf("rechecking=%t", rechecking), func(t *testing.T) {
			testutil.SetupDB(t)
			previousBuddy, previousPeer, previousRecheck := config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists
			config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = false, false, true
			t.Cleanup(func() {
				config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = previousBuddy, previousPeer, previousRecheck
			})
			hashes := make([]string, 201)
			for i := range hashes {
				hashes[i] = testutil.Hash(t)
			}
			if rechecking {
				_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('pm',?,1,unixepoch())", hashes[0])
				require.NoError(t, err)
			}
			var ready sync.WaitGroup
			ready.Add(3)
			failure := errors.New("concurrent provider failure")
			client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				ready.Done()
				ready.Wait()
				return nil, failure
			})}})
			params := &store.CheckMagnetParams{Magnets: hashes}
			params.APIKey = "fixture-token"
			result, err := client.CheckMagnet(params)
			if !rechecking {
				require.ErrorIs(t, err, failure)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.ErrorIs(t, result.RecheckError, failure)
			require.Len(t, result.Items, len(hashes))
			require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
			for _, item := range result.Items[1:] {
				require.Equal(t, store.MagnetStatusUnknown, item.Status)
			}
		})
	}
}

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
			_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNamePremiumize.Code()), hash)
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
		_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNamePremiumize.Code()), hash)
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

func TestInvalidCacheResponseCardinalityIsReported(t *testing.T) {
	originalPeer, originalRecheck := config.HasPeer, config.StoreRecheckMissingFileLists
	config.HasPeer, config.StoreRecheckMissingFileLists = false, true
	t.Cleanup(func() {
		config.HasPeer, config.StoreRecheckMissingFileLists = originalPeer, originalRecheck
	})
	testutil.SetupDB(t)
	fileless, missing := testutil.Hash(t), testutil.Hash(t)
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreNamePremiumize.Code()), fileless)
	require.NoError(t, err)
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.JSONResponse(r, 200, map[string]any{
			"status": "success", "response": []bool{true}, "filename": []string{}, "filesize": []int{1},
		}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{fileless}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.ErrorContains(t, result.RecheckError, "response=1 filename=0 filesize=1 for 1 magnets")
	require.Len(t, result.Items, 1)
	require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)

	params.Magnets = []string{missing}
	result, err = client.CheckMagnet(params)
	require.Nil(t, result)
	require.ErrorContains(t, err, "response=1 filename=0 filesize=1 for 1 magnets")
}

func TestPartialRecheckFailureKeepsNewFileEvidence(t *testing.T) {
	testutil.SetupDB(t)
	previousRecheck, previousBuddy, previousPeer, previousToken := config.StoreRecheckMissingFileLists, config.HasBuddy, config.HasPeer, config.PeerAuthToken
	originalBuddy, originalPeer := buddy.Buddy, buddy.Peer
	t.Cleanup(func() {
		config.StoreRecheckMissingFileLists, config.HasBuddy, config.HasPeer, config.PeerAuthToken = previousRecheck, previousBuddy, previousPeer, previousToken
		buddy.Buddy, buddy.Peer = originalBuddy, originalPeer
	})
	config.StoreRecheckMissingFileLists, config.HasBuddy, config.HasPeer, config.PeerAuthToken = true, true, true, "fixture-token"
	buddyTracked, peerTracked := make(chan struct{}, 1), make(chan struct{}, 1)
	trackingClient := func(done chan struct{}) *http.Client {
		return &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			done <- struct{}{}
			return testutil.JSONResponse(r, 200, map[string]any{"data": map[string]any{}}), nil
		})}
	}
	buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: trackingClient(buddyTracked)})
	buddy.Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: trackingClient(peerTracked)})
	first, second := testutil.Hash(t), testutil.Hash(t)
	recorded := torrent_stream.ObserveFileRecordingForTesting(first, []string{"/movie.mkv", "/setup.exe"})
	for _, hash := range []string{first, second} {
		_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('pm',?,1,unixepoch())", hash)
		require.NoError(t, err)
	}
	failure := errors.New("second file lookup failed")
	fileCalls := 0
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/cache/check") {
			return testutil.JSONResponse(r, 200, map[string]any{"status": "success", "response": []bool{true, true}, "filename": []string{"Movie", "Movie"}, "filesize": []int{101, 101}}), nil
		}
		fileCalls++
		if fileCalls == 2 {
			return nil, failure
		}
		return testutil.JSONResponse(r, 200, map[string]any{"status": "success", "content": []map[string]any{{"path": "Movie/movie.mkv", "size": 100}, {"path": "Movie/setup.exe", "size": 1}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{first, second}}
	params.APIKey = "fixture-token"
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.ErrorIs(t, result.RecheckError, failure)
	require.Len(t, result.Items, 2)
	require.Len(t, result.Items[0].Files, 2)
	require.Equal(t, "setup.exe", result.Items[0].Files[1].Name)
	require.Equal(t, store.MagnetStatusCached, result.Items[1].Status)
	require.Empty(t, result.Items[1].Files)
	for _, done := range []chan struct{}{buddyTracked, peerTracked} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("tracking did not complete")
		}
	}
	require.Eventually(t, recorded, 5*time.Second, 10*time.Millisecond, "rechecked evidence was not stored")
}
