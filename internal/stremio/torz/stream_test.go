package stremio_torz

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"path"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestGetStreamsForHashesFileSeason(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	db.SetForTesting(sqlDB, t.Cleanup)

	const seriesID = "tt9990001:13:10"
	series := torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "13", Episode: "10"}
	movie := torrent_stream.NormalizedStremId{Id: "tt9990002"}
	anime := torrent_stream.NormalizedStremId{Id: "9990003", Season: "1", Episode: "10", IsAnime: true}
	tests := []struct {
		name      string
		paths     []string
		sid, asid string
		request   string
		nsid      torrent_stream.NormalizedStremId
		title     string
		wantIndex int // -1 means no stream; indices start at 1
	}{
		{name: "wrong season release folder", paths: []string{"/Synthetic.Series.S05.1080p.WEB-DL/10 Synthetic Episode (1080p HD).m4v"}, wantIndex: -1},
		{name: "correct season release folder", paths: []string{"/Synthetic.Series.S13.1080p.WEB-DL/10 Synthetic Episode (1080p HD).m4v"}, wantIndex: 1},
		{name: "wrong season folder", paths: []string{"/Synthetic Series/Season 5/E10.mkv"}, wantIndex: -1},
		{name: "correct season folder", paths: []string{"/Synthetic Series/Season 13/E10.mkv"}, wantIndex: 1},
		{name: "episode only no season", paths: []string{"/Synthetic Series/E10.mkv"}, wantIndex: -1},
		{name: "missing file list without season", wantIndex: -1},
		{name: "missing file list with single season", title: "Synthetic.Series.S13.1080p", wantIndex: 0},
		{name: "episode only root", paths: []string{"/E10.mkv"}, wantIndex: -1},
		{name: "explicit filename season", paths: []string{"/Synthetic Series/S13E10.mkv"}, wantIndex: 1},
		{name: "wrong filename season", paths: []string{"/Synthetic Series/Season 13/S05E10.mkv"}, wantIndex: -1},
		{name: "filename overrides folder", paths: []string{"/Synthetic Series/Season 5/S13E10.mkv"}, wantIndex: 1},
		{name: "nested conflicting seasons", paths: []string{"/Synthetic.Series.S05/Season 13/E10.mkv"}, wantIndex: -1},
		{name: "season pack includes request", paths: []string{"/Synthetic.Series.S01-S13/E10.mkv"}, wantIndex: 1},
		{name: "season pack excludes request", paths: []string{"/Synthetic.Series.S01-S05/E10.mkv"}, wantIndex: -1},
		{name: "season pack with season folder", paths: []string{"/Synthetic.Series.S01-S13/Season 13/E10.mkv"}, wantIndex: 1},
		{name: "season pack with wrong season folder", paths: []string{"/Synthetic.Series.S01-S13/Season 5/E10.mkv"}, wantIndex: -1},
		{name: "explicit filename in season pack", paths: []string{"/Synthetic.Series.S01-S13/S13E10.mkv"}, wantIndex: 1},
		{name: "numbers are not seasons", paths: []string{"/2024/1080p/E10.mkv"}, wantIndex: -1},
		{name: "folder episode cannot replace filename episode", paths: []string{"/Synthetic.Series.S13E10/S13E09.mkv"}, wantIndex: -1},
		{name: "folder cannot supply missing episode", paths: []string{"/Synthetic.Series.S13E10/video.mkv"}, wantIndex: -1},
		{name: "non video", paths: []string{"/Synthetic Series/Season 13/E10.txt"}, wantIndex: -1},
		{name: "choose correct file among repeated episodes", paths: []string{"/Synthetic Series/Season 05/E10.mkv", "/Synthetic Series/Season 13/E10.mkv"}, wantIndex: 2},
		{name: "SID overrides path conflict", paths: []string{"/Synthetic Series/Season 5/S05E10.mkv"}, sid: seriesID, wantIndex: 1},
		{name: "ASID overrides path conflict", paths: []string{"/Synthetic Series/Season 5/S05E10.mkv"}, asid: seriesID, wantIndex: 1},
		{name: "explicit mapping beats fallback", paths: []string{"/Synthetic Series/Season 13/E10.mkv", "/Synthetic Series/Season 5/S05E10.mkv"}, sid: seriesID, wantIndex: 2},
		{name: "movie explicit mapping", paths: []string{"/Season 5/Synthetic.Movie.2024.mkv"}, sid: "tt9990002", request: "tt9990002", nsid: movie, wantIndex: 1},
		{name: "movie without mapping", paths: []string{"/Season 5/E10.mkv"}, request: "tt9990002", nsid: movie, wantIndex: 0},
		{name: "anime explicit mapping", paths: []string{"/Season 5/S05E10.mkv"}, asid: "9990003:10", request: "kitsu:9990003:10", nsid: anime, wantIndex: 1},
		{name: "anime without mapping", paths: []string{"/Season 5/E10.mkv"}, request: "kitsu:9990003:10", nsid: anime, wantIndex: 0},
		{name: "anime without normalized id", paths: []string{"/Season 5/E10.mkv"}, request: "kitsu:9990003:10", nsid: torrent_stream.NormalizedStremId{IsAnime: true, Episode: "10"}, wantIndex: 0},
		{name: "season pack title preserved", paths: []string{"/Synthetic Series/Season 13/E10.mkv"}, title: "Synthetic.Series.S13.1080p", wantIndex: 1},

		{name: "aspect ratio directory", paths: []string{"/Synthetic Series/16x9/Season 13/E10.mkv"}, wantIndex: 1},
		{name: "aspect ratio with explicit filename", paths: []string{"/Synthetic Series/16x9/S13E10.mkv"}, wantIndex: 1},
		{name: "aspect ratio only", paths: []string{"/Synthetic Series/4x3/E10.mkv"}, wantIndex: -1},
		{name: "bare episode style folder is unknown", paths: []string{"/Synthetic Series/1x02/E10.mkv"}, wantIndex: -1},
		{name: "episode style filename still matches", paths: []string{"/Synthetic Series/Season 5/13x10.mkv"}, wantIndex: 1},
		{name: "filename overrides nested conflicting folders", paths: []string{"/Synthetic.Series.S05/Season 2/S13E10.mkv"}, wantIndex: 1},
		{name: "filename overrides excluding season pack", paths: []string{"/Synthetic.Series.S01-S05/S13E10.mkv"}, wantIndex: 1},
		{name: "hyphenated wrong season folder", paths: []string{"/Synthetic Series/Season-05/E10.mkv"}, wantIndex: -1},
		{name: "hyphenated correct season folder", paths: []string{"/Synthetic Series/Season-13/E10.mkv"}, wantIndex: 1},
		{name: "filename overrides hyphenated season folder", paths: []string{"/Synthetic Series/Season-05/S13E10.mkv"}, wantIndex: 1},
		{name: "padded season folder matches", paths: []string{"/Synthetic Series/Season 005/E10.mkv"}, request: "tt9990001:5:10", nsid: torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "5", Episode: "10"}, wantIndex: 1},
		{name: "padded season folder rejects specials", paths: []string{"/Synthetic Series/Season 005/E10.mkv"}, request: "tt9990001:0:10", nsid: torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "0", Episode: "10"}, wantIndex: -1},
		{name: "specials season folder matches", paths: []string{"/Synthetic Series/Season 000/E10.mkv"}, request: "tt9990001:0:10", nsid: torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "0", Episode: "10"}, wantIndex: 1},
		{name: "large season folder rejects truncated season", paths: []string{"/Synthetic Series/Season 100/E10.mkv"}, request: "tt9990001:10:10", nsid: torrent_stream.NormalizedStremId{Id: "tt9990001", Season: "10", Episode: "10"}, wantIndex: -1},
		{name: "French season folder rejects mismatch", paths: []string{"/Synthetic Series/Saison 05/E10.mkv"}, wantIndex: -1},
		{name: "Russian season folder rejects mismatch", paths: []string{"/Synthetic Series/Сезон 05/E10.mkv"}, wantIndex: -1},
		{name: "ordinal season folder rejects mismatch", paths: []string{"/Synthetic Series/5th Season/E10.mkv"}, wantIndex: -1},
		{name: "localized suffix season folder rejects mismatch", paths: []string{"/Synthetic Series/5 сезон/E10.mkv"}, wantIndex: -1},
		{name: "aspect ratio before explicit season marker", paths: []string{"/Synthetic.16x9.S05/E10.mkv"}, wantIndex: -1},
		{name: "padded season pack includes request", paths: []string{"/Synthetic.Series.Seasons.001-013/E10.mkv"}, wantIndex: 1},
	}
	fixtureID := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(fixtureID+"/"+tt.name)))
			files := torrent_stream.Files{}
			for j, p := range tt.paths {
				f := torrent_stream.File{Path: p, Name: path.Base(p), Idx: j + 1, Size: 1234, SId: "*"}
				// Put mappings on the last file, to prove they take priority over fallback.
				if j == len(tt.paths)-1 {
					if tt.sid != "" {
						f.SId = tt.sid
					}
					f.ASId = tt.asid
				}
				files = append(files, f)
			}
			title := tt.title
			if title == "" {
				title = "Synthetic Series Complete 1080p"
			}
			require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{Hash: hash, TorrentTitle: title, Size: 1234, Source: torrent_info.TorrentInfoSourceTorrentio, Files: files}}, "", false))
			request, nsid, category := seriesID, series, "series"
			if tt.request != "" {
				request, nsid = tt.request, tt.nsid
			}
			if !nsid.IsSeries() {
				category = "movie"
			}
			streams, err := GetStreamsForHashes(category, request, []string{hash}, &nsid)
			require.NoError(t, err)
			if tt.wantIndex == -1 {
				require.Empty(t, streams)
				return
			}
			require.Len(t, streams, 1)
			if tt.wantIndex == 0 {
				require.Equal(t, -1, streams[0].FileIndex)
			} else {
				require.Equal(t, tt.wantIndex, streams[0].FileIndex)
				require.Equal(t, path.Base(tt.paths[tt.wantIndex-1]), streams[0].BehaviorHints.Filename)
				require.Equal(t, tt.paths[tt.wantIndex-1], streams[0].filePath)
			}
		})
	}
}

func TestTitleAwareSelectionAndPlayback(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	db.SetForTesting(sqlDB, t.Cleanup)
	const sid = "tt9998601:3:10"
	nsid := &torrent_stream.NormalizedStremId{Id: "tt9998601", Season: "3", Episode: "10"}
	require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: nsid.Id, Title: "Synthetic Series Season 2", OrigTitle: "Synthetic Original Sezonul 2", Type: "tvSeries"}}))
	tests := []struct {
		name, path, title string
		want              bool
	}{
		{"display title protected", "/S03/Synthetic.Series.Season.2/E10.mkv", "Synthetic Series S03", config.Feature.HasIMDBTitle()},
		{"original title protected", "/S03/Synthetic.Original.Sezonul.2/E10.mkv", "Synthetic Series S03", config.Feature.HasIMDBTitle()},
		{"explicit filename", "/Season 2/Synthetic.Series.Season.2.S03E10.mkv", "Synthetic Series S03", true},
		{"no season evidence", "/Synthetic Series/E10.mkv", "Synthetic Series Complete", false},
		{"single season torrent", "/Synthetic Series/E10.mkv", "Synthetic Series S03", true},
		{"whole series torrent", "/Synthetic Series/E10.mkv", "Synthetic Series S01-S03", false},
		{"torrent cannot undo conflict", "/Season 2/E10.mkv", "Synthetic Series S03", false},
		{"folder conflicts", "/S03/Synthetic Different Season 2/E10.mkv", "Synthetic Series S03", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
			files := torrent_stream.Files{{Path: tt.path, Idx: 1, Size: 1234, SId: "*"}}
			require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{Hash: hash, TorrentTitle: tt.title, Size: 1234, Source: torrent_info.TorrentInfoSourceTorrentio, Files: files}}, "", false))
			streams, err := GetStreamsForHashes("series", sid, []string{hash}, nsid)
			require.NoError(t, err)
			require.Equal(t, tt.want, len(streams) == 1)
			context := stremio_shared.LoadFileSeasonContext(sid)
			providerFile := &store.MagnetFile{Name: path.Base(tt.path), Path: tt.path, Idx: 42}
			played := stremio_shared.MatchFileByStremIdWithPath(tt.title, []store.File{providerFile}, sid, "", store.StoreCodeTorBox, tt.path, context)
			require.Equal(t, tt.want, played != nil, "listing and playback must agree")
		})
	}
	missing := stremio_shared.LoadFileSeasonContext("tt9998602:3:10")
	require.False(t, missing.MatchesSeason("E10.mkv", "/Synthetic Series/E10.mkv", 3))
	require.False(t, missing.MatchesSeason("E10.mkv", "/S03/Synthetic Series Season 2/E10.mkv", 3))
	require.True(t, missing.MatchesSeason("E10.mkv", "/Season 3/E10.mkv", 3))
}
