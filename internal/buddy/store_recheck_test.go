package buddy

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

type recheckTestStore struct{ store.Store }

func (recheckTestStore) GetName() store.StoreName { return store.StoreNameDebrider }

func TestCheckMagnetCombinesPeerChunkRecheckFailures(t *testing.T) {
	setupPullTorrentTestDB(t)
	oldPeer, oldHasPeer, oldHasBuddy, oldLazy := Peer, config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy
	config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy = true, false, false
	t.Cleanup(func() {
		Peer, config.HasPeer, config.HasBuddy, config.PeerFlag.Lazy = oldPeer, oldHasPeer, oldHasBuddy, oldLazy
	})
	hashes := make([]string, 501)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("bc%038x", i)
	}
	Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: pullTorrentRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		data := store.CheckMagnetData{Items: []store.CheckMagnetDataItem{}}
		for _, hash := range r.URL.Query()["magnet"] {
			data.Items = append(data.Items, store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown})
			if hash == hashes[0] {
				data.RecheckError = store.ErrCheckMagnetRecheckFailed
			}
		}
		return pullTorrentJSONResponse(r, 200, map[string]any{"data": data})
	})}})
	result, err := CheckMagnet(recheckTestStore{}, hashes, "fixture", "", "", true)
	require.NoError(t, err)
	require.Len(t, result.Items, len(hashes), "successful and failed chunks retain their answers")
	require.ErrorIs(t, result.RecheckError, store.ErrCheckMagnetRecheckFailed)
}
