package peer_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestCheckMagnetUsesCanonicalRequestAndResponseHashes(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	upper := strings.ToUpper(hash)
	files := []store.MagnetFile{{Name: "Movie.MKV", Path: "/UPPER/Movie.MKV"}}
	client := peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, []string{hash}, r.URL.Query()["magnet"])
		require.Equal(t, "1", r.URL.Query().Get("local_only"))
		return testutil.JSONResponse(r, 200, map[string]any{"data": store.CheckMagnetData{Items: []store.CheckMagnetDataItem{{Hash: upper, Status: store.MagnetStatusCached, Files: files}}}}), nil
	})}})
	params := &peer.CheckMagnetParams{CheckMagnetParams: store.CheckMagnetParams{Magnets: []string{upper, hash}}}
	result, err := client.CheckMagnet(params)
	require.NoError(t, err)
	require.Equal(t, hash, result.Data.Items[0].Hash)
	require.Equal(t, files, result.Data.Items[0].Files)
	require.Equal(t, []string{upper, hash}, params.Magnets)
}
