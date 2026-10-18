package stremio_shared

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"regexp"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestPlaybackDuplicateEpisodeNames(t *testing.T) {
	wrong := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 5/E10.mkv", Idx: 1}
	right := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 13/E10.mkv", Idx: 2}
	files := []store.File{wrong, right}
	require.Nil(t, MatchFileByName(files, "E10.mkv"))
	_, ambiguous := LookupFileByName(files, "E10.mkv")
	require.True(t, ambiguous)
	require.Same(t, right, MatchFileByPath(files, "Season 13/E10.mkv"))
	require.Same(t, right, matchFileByIMDBStremId(files, "tt9990001:13:10"))
	require.Same(t, wrong, matchFileByIMDBStremId(files, "tt9990001:5:10"))
	require.Nil(t, matchFileByIMDBStremId(files, "tt9990001:2:10"))
	require.Same(t, right, MatchFileByIdx(files, 2, store.StoreCodeRealDebrid))
	require.Nil(t, MatchFileByIdx(files, 2, store.StoreCodeTorBox))
	require.Same(t, right, MatchFileByName([]store.File{right}, "E10.mkv"))
	_, ambiguous = LookupFileByName([]store.File{right}, "E10.mkv")
	require.False(t, ambiguous)
}

func TestIMDbPlaybackSeasonEvidence(t *testing.T) {
	tests := []struct {
		name, fileName, filePath string
		want                     bool
	}{
		{"explicit filename wins", "S13E10.mkv", "/Season 5/S13E10.mkv", true},
		{"filename mismatch wins", "S05E10.mkv", "/Season 13/S05E10.mkv", false},
		{"matching folder", "E10.mkv", "/Sezonul 13/E10.mkv", true},
		{"wrong folder", "E10.mkv", "/Sezonul 5/E10.mkv", false},
		{"unknown season", "E10.mkv", "/Synthetic/E10.mkv", false},
		{"aspect ratio", "E10.mkv", "/16x9/E10.mkv", false},
		{"wrong episode", "S13E09.mkv", "/Season 13/S13E09.mkv", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &store.MagnetFile{Name: tt.fileName, Path: tt.filePath}
			found := matchFileByIMDBStremId([]store.File{f}, "tt9990001:13:10")
			require.Equal(t, tt.want, found != nil)
		})
	}
	unknowns := []store.File{&store.MagnetFile{Name: "E10.mkv", Path: "/One/E10.mkv"}, &store.MagnetFile{Name: "E10.mkv", Path: "/Two/E10.mkv"}}
	require.Nil(t, matchFileByIMDBStremId(unknowns, "tt9990001:13:10"))
}

func TestLargestFileFallbackSeason(t *testing.T) {
	wrong := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 5/E10.mkv", Size: 100}
	require.Nil(t, MatchFileByLargestSizeForStremId([]store.File{wrong}, "tt9990001:13:10"))
	require.Same(t, wrong, MatchFileByLargestSizeForStremId([]store.File{wrong}, "tt9990001:5:10"))
	require.Same(t, wrong, MatchFileByLargestSizeForStremId([]store.File{wrong}, "tt9990001"))
	require.Same(t, wrong, MatchFileByLargestSizeForStremId([]store.File{wrong}, "kitsu:9990001:10"))
	explicit := &store.MagnetFile{Name: "S13E10.mkv", Path: "/Season 5/S13E10.mkv", Size: 100}
	require.Same(t, explicit, MatchFileByLargestSizeForStremId([]store.File{explicit}, "tt9990001:13:10"))
}

func TestMappedPlaybackUsesPathBeforeProviderIndex(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	db.SetForTesting(sqlDB, t.Cleanup)
	hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/Season 13/E10.mkv", Idx: 2, SId: "tt9990001:13:10", Source: "tor"}}}, false))
	wrong := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 5/E10.mkv", Idx: 2}
	right := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 13/E10.mkv", Idx: 9}
	files := []store.File{wrong, right}
	require.Same(t, right, MatchFileByStremId("Synthetic", files, "tt9990001:13:10", hash, store.StoreCodeRealDebrid))
	require.Same(t, right, MatchFileByStremId("Synthetic", files, "tt9990001:13:10", hash, store.StoreCodeTorBox))
	require.Same(t, right, MatchFileByStremIdWithPath("Synthetic", files, "tt9990001:13:10", hash, store.StoreCodeTorBox, wrong.Path), "explicit mappings beat path hints")
}

func TestPlaybackFallbacksCannotUndoSeasonRejection(t *testing.T) {
	const sid = "tt9990001:13:10"
	files := []store.File{&store.MagnetFile{Name: "E10.mkv", Path: "/Season 5/E10.mkv", Idx: 1}}
	require.Nil(t, MatchFileByStremId("Synthetic", files, sid, "", store.StoreCodeRealDebrid))
	require.Nil(t, FilterFileByStremSeason(MatchFileByName(files, "E10.mkv"), sid))
	require.Nil(t, FilterFileByStremSeason(MatchFileByIdx(files, 1, store.StoreCodeRealDebrid), sid))
	require.Nil(t, FilterFileByStremSeason(MatchFileByPattern(files, regexp.MustCompile(`E10`)), sid))
	require.Nil(t, MatchFileByLargestSizeForStremId(files, sid))
	require.Nil(t, MatchFileByStremIdWithPath("Synthetic", files, sid, "", store.StoreCodeRealDebrid, files[0].GetPath()))
	explicit := &store.MagnetFile{Name: "S13E10.mkv", Path: "/Season 5/S13E10.mkv"}
	require.Same(t, explicit, FilterFileByStremSeason(explicit, sid), "filename season wins over folders")
	require.Same(t, files[0], FilterFileByStremSeason(files[0], "tt9990001"))
	require.Same(t, files[0], FilterFileByStremSeason(files[0], "kitsu:9990001:10"))
}

func TestPlaybackSelectedPathResolvesDuplicateNames(t *testing.T) {
	a := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 13/Version A/E10.mkv", Idx: 10}
	b := &store.MagnetFile{Name: "E10.mkv", Path: "/Season 13/Version B/E10.mkv", Idx: 20}
	files := []store.File{a, b}
	require.Nil(t, MatchFileByStremId("Synthetic", files, "tt9990001:13:10", "", store.StoreCodeTorBox))
	require.Same(t, a, MatchFileByStremIdWithPath("Synthetic", files, "tt9990001:13:10", "", store.StoreCodeTorBox, a.Path))
	require.Same(t, b, MatchFileByStremIdWithPath("Synthetic", files, "tt9990001:13:10", "", store.StoreCodeTorBox, b.Path))
}

func TestPlaybackFallbackSearchesSeasonEligibleFiles(t *testing.T) {
	wrong := &store.MagnetFile{Name: "Feature.mkv", Path: "/Season 5/Feature.mkv", Size: 100}
	right := &store.MagnetFile{Name: "Feature.mkv", Path: "/Season 13/Feature.mkv", Size: 90}
	other := &store.MagnetFile{Name: "Other.mkv", Path: "/Season 13/Other.mkv", Size: 95}
	for _, files := range [][]store.File{{wrong, right, other}, {other, right, wrong}} {
		require.Same(t, right, MatchFileByPatternForStremId(files, regexp.MustCompile(`Feature`), "tt9990001:13:10"))
		require.Same(t, other, MatchFileByLargestSizeForStremId(files, "tt9990001:13:10"))
		for _, sid := range []string{"tt9990001", "kitsu:9990001:10", "mal:9990001:10", "anidb:9990001:10"} {
			require.Same(t, wrong, MatchFileByLargestSizeForStremId(files, sid))
			require.Equal(t, MatchFileByPattern(files, regexp.MustCompile(`Feature`)), MatchFileByPatternForStremId(files, regexp.MustCompile(`Feature`), sid))
		}
	}
	require.Nil(t, MatchFileByPatternForStremId([]store.File{wrong}, regexp.MustCompile(`Feature`), "tt9990001:13:10"))
	require.Nil(t, MatchFileByLargestSizeForStremId([]store.File{wrong}, "tt9990001:13:10"))
	require.Nil(t, MatchFileByPatternForStremId([]store.File{right}, nil, "tt9990001:13:10"))
}

func TestPlaybackFallbacksRequirePositiveSeasonEvidence(t *testing.T) {
	const sid = "tt9998603:3:10"
	file := &store.MagnetFile{Name: "E10.mkv", Path: "/Synthetic Series/E10.mkv", Idx: 1, Size: 100}
	files := []store.File{file}
	for _, title := range []string{"", "Synthetic Series Complete", "Synthetic Series S01-S03", "Synthetic Series S03"} {
		context := NewFileSeasonContext("Synthetic Series").WithTorrentTitle(title)
		want := title == "Synthetic Series S03"
		require.Equal(t, want, FilterFileByStremSeason(file, sid, context) != nil, title)
		require.Equal(t, want, MatchFileByLargestSizeForStremId(files, sid, context) != nil, title)
		require.Equal(t, want, MatchFileByPatternForStremId(files, regexp.MustCompile(`E10`), sid, context) != nil, title)
		require.Equal(t, want, MatchFileByStremIdWithPath(title, files, sid, "", store.StoreCodeTorBox, file.Path, context) != nil, title)
	}
}
