package stremio_shared

import (
	"fmt"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestStoredPlaybackRequiresProviderIndexProvenance(t *testing.T) {
	for _, source := range []string{"tor", "dht", "", "rd"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("source=%s/mixed=%t", source, mixed), func(t *testing.T) {
				testutil.SetupDB(t)
				hash := testutil.Hash(t)
				require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
					{Hash: hash, File: torrent_stream.File{Path: "/movie.mkv", Idx: 0, Source: source}},
					{Hash: hash, File: torrent_stream.File{Path: "/setup.exe", Idx: 1, Source: source}},
				}, false))
				// Torrent offsets 0/1 can correspond to RealDebrid file IDs 1/2.
				request := PlaybackFileRequest{Index: 1, StoreCode: store.StoreCodeRealDebrid, AllowMixed: mixed}
				blocked, err := StoredPlaybackIsBlocked(hash, request)
				require.NoError(t, err)
				require.Equal(t, !mixed || source == "rd", blocked)

				request.Name = "setup.exe"
				blocked, err = StoredPlaybackIsBlocked(hash, request)
				require.NoError(t, err)
				require.True(t, blocked, "explicit blocked names reject regardless of index provenance")
			})
		}
	}
}
