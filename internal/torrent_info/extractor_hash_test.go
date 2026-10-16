package torrent_info

import (
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/stretchr/testify/require"
)

func TestExtractCreateDataNormalizesTorrentioHashWithoutChangingMetadata(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	for _, fromURL := range []bool{false, true} {
		stream := stremio.Stream{Name: "[RD] Torrentio", InfoHash: strings.ToUpper(hash), Description: "Movie.Title", BehaviorHints: &stremio.StreamBehaviorHints{Filename: "Movie.MKV"}}
		if fromURL {
			stream.InfoHash = ""
			stream.URL = "https://fixture.invalid/" + strings.ToUpper(hash) + "/file/1/"
		}
		data := ExtractCreateDataFromStream("torrentio.strem.fun", "tt1234567", &stream)
		require.NotNil(t, data)
		require.Equal(t, hash, data.Hash)
		require.Equal(t, "Movie.Title", data.TorrentTitle)
		require.Equal(t, "Movie.MKV", data.Files[0].Name)
	}
}
