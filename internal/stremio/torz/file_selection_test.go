package stremio_torz

import (
	"testing"

	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/stretchr/testify/require"
)

// Indexers can supply fresh files without a stored torrent or normalized names.
func TestSelectTorrentFileFromIndexer(t *testing.T) {
	nsid := &torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "13", Episode: "10"}
	files := torrent_stream.Files{
		{Path: "/Sezonul 5/E10.mkv", Idx: 1},
		{Path: "/Sezonul 13/E10.mkv", Idx: 2},
	}
	file, allowed := selectTorrentFile(files, "tt9990001:13:10", nsid)
	require.True(t, allowed)
	require.NotNil(t, file)
	require.Equal(t, 2, file.Idx)
	require.Empty(t, files[0].Name, "cached indexer file lists must not be mutated")
	_, allowed = selectTorrentFile(files[:1], "tt9990001:13:10", nsid)
	require.False(t, allowed)
	files[0].SId = "tt9990001:13:10"
	file, allowed = selectTorrentFile(files, "tt9990001:13:10", nsid)
	require.True(t, allowed)
	require.Equal(t, 1, file.Idx, "explicit mappings override filename/folder inference")
	file, allowed = selectTorrentFile(nil, "tt9990001:13:10", nsid)
	require.False(t, allowed, "an absent list supplies no season evidence")
	require.Nil(t, file)
}

func TestIndexerPreservesStoredExplicitMappings(t *testing.T) {
	for _, anime := range []bool{false, true} {
		t.Run(map[bool]string{false: "IMDb SID", true: "anime ASID"}[anime], func(t *testing.T) {
			sid := "tt9990001:13:10"
			nsid := &torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "13", Episode: "10"}
			stored := torrent_stream.Files{{Path: "/Season 5/S05E20.mkv", Idx: 9, SId: sid}}
			if anime {
				sid = "kitsu:9990003:10"
				nsid = &torrent_stream.NormalizedStremId{Id: "9990003", Season: "1", Episode: "10", IsAnime: true}
				stored[0].SId = "*"
				stored[0].ASId = "9990003:10"
			}
			incoming := torrent_stream.Files{{Path: "/Season 13/S13E10.mkv", Idx: 1, SId: "tt9990001:13:10", ASId: "9990003:10"}}
			for _, files := range []torrent_stream.Files{nil, incoming} {
				file, allowed := selectIndexerTorrentFile(files, stored, sid, nsid)
				require.True(t, allowed)
				require.NotNil(t, file)
				require.Equal(t, 9, file.Idx)
				require.Equal(t, "S05E20.mkv", file.Name)
				require.Empty(t, stored[0].Name, "stored lists must not be mutated")
			}
		})
	}
	nsid := &torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "13", Episode: "10"}
	stored := torrent_stream.Files{{Path: "/Season 13/E09.mkv", Idx: 9, SId: "tt9990001:13:9"}}
	incoming := torrent_stream.Files{{Path: "/Season 13/E10.mkv", Idx: 1}}
	file, allowed := selectIndexerTorrentFile(incoming, stored, "tt9990001:13:10", nsid)
	require.True(t, allowed)
	require.NotNil(t, file)
	require.Equal(t, 1, file.Idx, "unrelated mappings must not suppress indexer inference")
}

func TestTitleSeasonWordCannotHideEpisodeOnlyFile(t *testing.T) {
	nsid := &torrent_stream.NormalizedStremId{Id: "tt9998501", Season: "1", Episode: "2"}
	const sid = "tt9998501:1:2"
	for _, folder := range []string{"Synthetic.Four.Seasons.2024", "Synthetic.Last.Season.2", "Synthetic.Saisons.2024", "Synthetic.Sezonul.2024"} {
		t.Run(folder, func(t *testing.T) {
			files := torrent_stream.Files{{Path: "/" + folder + "/Season 1/E02.mkv", Name: "E02.mkv", Idx: 9}}
			selected, allowed := selectTorrentFile(files, sid, nsid, stremio_shared.NewFileSeasonContext(folder))
			require.True(t, allowed)
			require.NotNil(t, selected)
			require.Equal(t, 9, selected.Idx)
			files[0].Path = "/" + folder + "/Season 5/E02.mkv"
			_, allowed = selectTorrentFile(files, sid, nsid, stremio_shared.NewFileSeasonContext(folder))
			require.False(t, allowed)
			files[0].Name = "S01E02.mkv"
			selected, allowed = selectTorrentFile(files, sid, nsid, stremio_shared.NewFileSeasonContext(folder))
			require.True(t, allowed, "explicit filename season still takes precedence")
			require.NotNil(t, selected)
		})
	}
}

func TestEpisodeOnlyFileRespectsTrailingFolderSeason(t *testing.T) {
	nsid := &torrent_stream.NormalizedStremId{Id: "tt9998502", Season: "3", Episode: "10"}
	const sid = "tt9998502:3:10"
	files := torrent_stream.Files{{Path: "/Synthetic Series Season 2/E10.mkv", Idx: 1}}
	_, allowed := selectTorrentFile(files, sid, nsid)
	require.False(t, allowed, "episode 10 alone must not match season 3 against folder season 2")
	files[0].Path = "/Synthetic Series Season 3/E10.mkv"
	selected, allowed := selectTorrentFile(files, sid, nsid)
	require.True(t, allowed)
	require.NotNil(t, selected)
	files[0].Path = "/Synthetic Series Season 2/S03E10.mkv"
	selected, allowed = selectTorrentFile(files, sid, nsid)
	require.True(t, allowed, "explicit filename season retains precedence")
	require.NotNil(t, selected)
	files[0].Path = "/Synthetic Series Season 2/E10.mkv"
	files[0].SId = sid
	selected, allowed = selectTorrentFile(files, sid, nsid)
	require.True(t, allowed, "explicit mapping retains precedence")
	require.NotNil(t, selected)
}
