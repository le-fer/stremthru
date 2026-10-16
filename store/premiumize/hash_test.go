package premiumize

import (
	"net/http"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestListMagnetsNormalizesCachedFolderHash(t *testing.T) {
	testutil.SetupDB(t)
	hash := testutil.Hash(t)
	require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{
		Hash: hash, TorrentTitle: "Movie.2024.1080p", Size: 123,
		Source: torrent_info.TorrentInfoSourceIndexer,
	}}, torrent_info.TorrentInfoCategoryMovie, true))
	id := CachedMagnetIdPrefix + strings.ToUpper(hash)
	client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/folder/search"):
			return testutil.JSONResponse(r, 200, map[string]any{"status": "success", "content": []map[string]any{{"id": "providerID", "name": id}}}), nil
		case strings.HasSuffix(r.URL.Path, "/transfer/list"):
			return testutil.JSONResponse(r, 200, map[string]any{"status": "success", "transfers": []any{}}), nil
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			return testutil.JSONResponse(r, 404, map[string]any{}), nil
		}
	})}})
	params := &store.ListMagnetsParams{Limit: 100}
	params.APIKey = "fixture"
	data, err := client.ListMagnets(params)
	require.NoError(t, err)
	require.Len(t, data.Items, 1)
	require.Equal(t, hash, data.Items[0].Hash)
	require.Equal(t, id, data.Items[0].Id, "provider identifiers must retain their original spelling")
	require.Equal(t, "Movie.2024.1080p", data.Items[0].Name)
	require.EqualValues(t, 123, data.Items[0].Size)
}
