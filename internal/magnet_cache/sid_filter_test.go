package magnet_cache

import (
	"testing"

	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/stretchr/testify/require"
)

func TestMatchesSidFilterDistinguishesMissingFilesFromMismatches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sid    string
		cached bool
		files  torrent_stream.Files
		want   bool
	}{
		{"no filter", "", true, torrent_stream.Files{{SId: "tt1234567:1:3"}}, true},
		{"fileless cached", "tt1234567:1:2", true, nil, true},
		{"noncached evidence", "tt1234567:1:2", false, torrent_stream.Files{{SId: "tt1234567:1:3"}}, true},
		{"matching episode", "tt1234567:1:2", true, torrent_stream.Files{{SId: "tt1234567:1:2"}}, true},
		{"wildcard", "tt1234567:1:2", true, torrent_stream.Files{{SId: "*"}}, true},
		{"different episode", "tt1234567:1:2", true, torrent_stream.Files{{SId: "tt1234567:1:3"}}, false},
		{"supplied file without mapping", "tt1234567:1:2", true, torrent_stream.Files{{SId: ""}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchesSidFilter(&MagnetCache{IsCached: tc.cached, Files: tc.files}, tc.sid))
		})
	}
}
