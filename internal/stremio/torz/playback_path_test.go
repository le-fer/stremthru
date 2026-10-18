package stremio_torz

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestSelectedFilePlaybackRoundTrip(t *testing.T) {
	const sid = "tt9990001:13:10"
	nsid := &torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "13", Episode: "10"}
	for _, selectedPath := range []string{
		"/Season 13/Version A/E10.mkv",
		"/Season 13/Version B/E10.mkv",
		"/Season 13/Spécial + # & ? %/E10.mkv",
	} {
		t.Run(selectedPath, func(t *testing.T) {
			files := torrent_stream.Files{
				{Name: "E10.mkv", Path: selectedPath, Idx: 1, SId: "*"},
				{Name: "E10.mkv", Path: "/Season 13/Other/E10.mkv", Idx: 2, SId: "*"},
			}
			selected, allowed := selectTorrentFile(files, sid, nsid)
			require.True(t, allowed)
			require.NotNil(t, selected)
			wrapped := WrappedStream{
				filePath: selected.Path,
				R:        &stremio_transformer.StreamExtractorResult{File: stremio_transformer.StreamExtractorResultFile{Name: selected.Name, Idx: selected.Idx}},
			}
			base, err := url.Parse("https://example.invalid/stremio/torz/test/_/strem/" + sid)
			require.NoError(t, err)
			originalBase := base.String()
			endpoint := wrapped.playbackURL(base, "torbox", strings.Repeat("a", 40))
			req := httptest.NewRequest("GET", endpoint.String(), nil)
			require.Equal(t, selectedPath, req.URL.Query().Get("filePath"))
			require.Equal(t, originalBase, base.String(), "building one URL must not change subsequent URLs")
			providerFiles := []store.File{
				&store.MagnetFile{Name: "E10.mkv", Path: files[1].Path, Idx: 21},
				&store.MagnetFile{Name: "E10.mkv", Path: selectedPath, Idx: 99},
			}
			played := stremio_shared.MatchFileByStremIdWithPath("Synthetic", providerFiles, sid, "", store.StoreCodeTorBox, req.URL.Query().Get("filePath"))
			require.NotNil(t, played)
			require.Equal(t, selectedPath, played.GetPath())
			require.Equal(t, 99, played.GetIdx())
			require.Equal(t, "E10.mkv", wrapped.R.File.Name)
		})
	}
}

func TestPlaybackURLWithoutPathKeepsLegacyShape(t *testing.T) {
	base, err := url.Parse("https://example.invalid/strem/tt9990001:13:10")
	require.NoError(t, err)
	wrapped := WrappedStream{R: &stremio_transformer.StreamExtractorResult{File: stremio_transformer.StreamExtractorResultFile{Name: "S13E10.mkv", Idx: 3}}}
	endpoint := wrapped.playbackURL(base, "torbox", "synthetic-hash")
	require.Equal(t, "", endpoint.RawQuery)
	require.Equal(t, "/strem/tt9990001:13:10/torbox/synthetic-hash/3/S13E10.mkv", endpoint.Path)
}
