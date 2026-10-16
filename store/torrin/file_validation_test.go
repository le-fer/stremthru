package torrin

import (
	"net/http"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestCheckMagnetForwardsLocalOnlyAndPreservesFiles(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		hash := "0123456789012345678901234567890123456789"
		files := []store.MagnetFile{{Name: "movie.mkv", Path: "/movie.mkv"}, {Name: "setup.exe", Path: "/setup.exe"}}
		client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "/v0/store/magnets/check", r.URL.Path)
			require.Equal(t, []string{hash}, r.URL.Query()["magnet"])
			require.Equal(t, "tt1234567", r.URL.Query().Get("sid"))
			require.Equal(t, "192.0.2.1", r.URL.Query().Get("client_ip"))
			if localOnly {
				require.Equal(t, "1", r.URL.Query().Get("local_only"))
			} else {
				require.False(t, r.URL.Query().Has("local_only"))
			}
			return testutil.JSONResponse(r, 200, map[string]any{"data": store.CheckMagnetData{Items: []store.CheckMagnetDataItem{{Hash: hash, Status: store.MagnetStatusCached, Files: files}}}}), nil
		})}})
		params := &store.CheckMagnetParams{Magnets: []string{hash}, SId: "tt1234567", ClientIP: "192.0.2.1", LocalOnly: localOnly}
		params.APIKey = "fixture-token"
		data, err := client.CheckMagnet(params)
		require.NoError(t, err)
		require.Len(t, data.Items, 1)
		require.Equal(t, files, data.Items[0].Files)
		require.Nil(t, params.Query, "forwarding must not mutate the caller's context")
	}
}

func TestCheckMagnetPreservesRemoteRecheckFailure(t *testing.T) {
	hash := "0123456789012345678901234567890123456789"
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.JSONResponse(r, http.StatusOK, map[string]any{"data": map[string]any{
			"items": []store.CheckMagnetDataItem{
				{Hash: hash, Status: store.MagnetStatusCached},
				{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", Status: store.MagnetStatusUnknown},
			},
			"recheck_failed": true,
		}}), nil
	})}})

	data, err := client.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}})
	require.NoError(t, err)
	require.Len(t, data.Items, 2)
	require.Equal(t, store.MagnetStatusCached, data.Items[0].Status)
	require.Equal(t, store.MagnetStatusUnknown, data.Items[1].Status)
	require.ErrorIs(t, data.RecheckError, store.ErrCheckMagnetRecheckFailed)
}
