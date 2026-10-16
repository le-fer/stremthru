package debrider

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestFilelessRecheckPreservesCachedAnswerWithSid(t *testing.T) {
	for _, sid := range []string{"", "tt1234567", "tt1234567:1:2"} {
		for _, localOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("sid=%s/local=%t", sid, localOnly), func(t *testing.T) {
				testutil.SetupDB(t)
				oldPeer, oldBuddy, oldRecheck := config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists
				config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists = false, false, true
				t.Cleanup(func() {
					config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists = oldPeer, oldBuddy, oldRecheck
				})
				hash := testutil.Hash(t)
				_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreCodeDebrider), hash)
				require.NoError(t, err)
				failure := errors.New("provider unavailable")
				calls := 0
				client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					return nil, failure
				})}})
				params := &store.CheckMagnetParams{Magnets: []string{hash}, SId: sid, LocalOnly: localOnly}
				params.APIKey = "fixture"
				result, err := client.CheckMagnet(params)
				require.NoError(t, err)
				require.Len(t, result.Items, 1)
				require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
				require.Empty(t, result.Items[0].Files)
				if localOnly {
					require.Zero(t, calls)
					require.NoError(t, result.RecheckError)
				} else {
					require.Equal(t, 1, calls)
					require.ErrorIs(t, result.RecheckError, failure)
				}
			})
		}
	}
}

func TestSidMismatchDoesNotBecomeFilelessRecheckFallback(t *testing.T) {
	testutil.SetupDB(t)
	oldPeer, oldBuddy, oldRecheck := config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists
	config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists = false, false, true
	t.Cleanup(func() {
		config.HasPeer, config.HasBuddy, config.StoreRecheckMissingFileLists = oldPeer, oldBuddy, oldRecheck
	})
	hash := testutil.Hash(t)
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/Series.S01E03.mkv", SId: "tt1234567:1:3"}}}, false))
	_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(store.StoreCodeDebrider), hash)
	require.NoError(t, err)
	failure := errors.New("provider unavailable")
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, failure })}})
	params := &store.CheckMagnetParams{Magnets: []string{hash}, SId: "tt1234567:1:2"}
	params.APIKey = "fixture"
	result, err := client.CheckMagnet(params)
	require.ErrorIs(t, err, failure)
	require.Nil(t, result, "a known episode mismatch must not be reused after a lookup failure")
}
