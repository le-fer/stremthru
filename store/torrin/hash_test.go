package torrin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestMagnetResponsesPreserveRemoteHashesIDsAndFiles(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	remoteHash := strings.ToUpper(hash)
	files := []store.MagnetFile{{Name: "Movie.MKV", Path: "/UPPER/Movie.MKV"}}
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		item := map[string]any{"id": "CaseSensitiveID", "hash": strings.ToUpper(hash), "files": files}
		var data any = item
		if r.URL.Path == "/v0/store/magnets/check" || (r.URL.Path == "/v0/store/magnets" && r.Method == http.MethodGet) {
			data = map[string]any{"items": []any{item}}
		}
		return testutil.JSONResponse(r, 200, map[string]any{"data": data}), nil
	})}})
	checked, err := client.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}})
	require.NoError(t, err)
	require.Equal(t, remoteHash, checked.Items[0].Hash)
	require.Equal(t, files, checked.Items[0].Files)
	added, err := client.AddMagnet(&store.AddMagnetParams{Magnet: hash})
	require.NoError(t, err)
	require.Equal(t, remoteHash, added.Hash)
	require.Equal(t, "CaseSensitiveID", added.Id)
	require.Equal(t, files, added.Files)
	got, err := client.GetMagnet(&store.GetMagnetParams{Id: "CaseSensitiveID"})
	require.NoError(t, err)
	require.Equal(t, remoteHash, got.Hash)
	require.Equal(t, "CaseSensitiveID", got.Id)
	require.Equal(t, files, got.Files)
	listed, err := client.ListMagnets(&store.ListMagnetsParams{})
	require.NoError(t, err)
	require.Equal(t, remoteHash, listed.Items[0].Hash)
	require.Equal(t, "CaseSensitiveID", listed.Items[0].Id)
}
