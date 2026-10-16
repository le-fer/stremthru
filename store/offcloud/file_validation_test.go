package offcloud

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestCheckMagnetRequestsAndRetainsFiles(t *testing.T) {
	hash := "0123456789012345678901234567890123456789"
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/cache/info", r.URL.Path)
		var body GetCacheInfoParams
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.True(t, body.IncludeFiles)
		require.Equal(t, []string{"magnet:?xt=urn:btih:" + hash}, body.URLs)
		return testutil.JSONResponse(r, 200, []map[string]any{{"cached": true, "files": []map[string]any{{"folder": []string{"Movie"}, "filename": "movie.mkv"}, {"folder": []string{"Movie"}, "filename": "setup.exe"}}}}), nil
	})}})
	params := &store.CheckMagnetParams{Magnets: []string{hash}}
	params.APIKey = "fixture-token"
	data, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.Len(t, data.Items, 1)
	require.Equal(t, store.MagnetStatusCached, data.Items[0].Status)
	require.Equal(t, []store.MagnetFile{{Idx: -1, Name: "movie.mkv", Path: "/Movie/movie.mkv", Size: -1, Source: "oc"}, {Idx: -1, Name: "setup.exe", Path: "/Movie/setup.exe", Size: -1, Source: "oc"}}, data.Items[0].Files)
}
