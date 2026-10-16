package stremio_torz

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestGetStreamsForHashesRejectsBlockedFiles(t *testing.T) {
	torrent_stream.ResetCachesForTesting(t.Cleanup)
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	db.SetForTesting(sqlDB, t.Cleanup)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	blockedHash := strings.Repeat("c", 40)
	allowedHash := strings.Repeat("d", 40)
	for _, hash := range []string{blockedHash, allowedHash} {
		_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
		require.NoError(t, err)
	}
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
		{Hash: blockedHash, File: torrent_stream.File{Path: "/movie.mkv", Name: "movie.mkv", Idx: 0, SId: "tt1234567"}},
		{Hash: blockedHash, File: torrent_stream.File{Path: "/setup.sh", Name: "setup.sh", Idx: 1}},
		{Hash: allowedHash, File: torrent_stream.File{Path: "/movie.mkv", Name: "movie.mkv", Idx: 0, SId: "tt1234567"}},
		{Hash: allowedHash, File: torrent_stream.File{Path: "/subtitle.srt", Name: "subtitle.srt", Idx: 1}},
	}, false))
	nsid, err := torrent_stream.NormalizeStreamId("tt1234567")
	require.NoError(t, err)
	previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
	config.AllowVideoTorrentsWithDisallowedFiles = false
	streams, err := GetStreamsForHashes("movie", "tt1234567", []string{blockedHash, allowedHash}, nsid)
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Equal(t, allowedHash, streams[0].R.Hash)
	config.AllowVideoTorrentsWithDisallowedFiles = true
	streams, err = GetStreamsForHashes("movie", "tt1234567", []string{blockedHash, allowedHash}, nsid)
	require.NoError(t, err)
	require.Len(t, streams, 2)
	for _, stream := range streams {
		require.Equal(t, "movie.mkv", stream.R.File.Name)
	}
	previousExtensions := util.FileExtDisallowed
	t.Cleanup(func() { util.FileExtDisallowed = previousExtensions })
	util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
	require.NoError(t, err)
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
		{Hash: blockedHash, File: torrent_stream.File{Name: "movie.mp4", Path: "/movie.mp4", Idx: 2, SId: "tt1234567"}},
	}, false))
	streams, err = GetStreamsForHashes("movie", "tt1234567", []string{blockedHash, allowedHash}, nsid)
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Equal(t, blockedHash, streams[0].R.Hash)
	require.Equal(t, "movie.mp4", streams[0].R.File.Name)
}

func TestGetStreamsForHashesPreservesCheckedFileIndex(t *testing.T) {
	torrent_stream.ResetCachesForTesting(t.Cleanup)
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	db.SetForTesting(sqlDB, t.Cleanup)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))

	hash := strings.Repeat("e", 40)
	_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Show.S01E02.1080p", "tb", "Show.S01E02.1080p")
	require.NoError(t, err)
	require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{
		Hash: hash,
		File: torrent_stream.File{Path: "/readme.txt", Name: "readme.txt", Idx: 0},
	}}, false))

	nsid, err := torrent_stream.NormalizeStreamId("tt1234567:1:2")
	require.NoError(t, err)
	streams, err := GetStreamsForHashesWithCheckedFiles("series", "tt1234567:1:2", []string{hash}, nsid, map[string][]store.MagnetFile{
		hash: {{Path: "/Show.S01E02.mkv", Name: "Show.S01E02.mkv", Idx: 7, Source: "rd"}},
	})
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Equal(t, 7, streams[0].R.File.Idx)
	require.Equal(t, 7, streams[0].Stream.FileIndex)

	missingFilesHash := strings.Repeat("f", 40)
	_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", missingFilesHash, "Show.S01E02.1080p", "tb", "Show.S01E02.1080p")
	require.NoError(t, err)

	candidates, err := GetStreamCandidatesForHashes("series", "tt1234567:1:2", []string{missingFilesHash}, nsid)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.True(t, candidates[0].FileListDeferred)

	compatible, err := GetStreamsForHashesWithCheckedFiles("series", "tt1234567:1:2", []string{missingFilesHash}, nsid, nil)
	require.NoError(t, err)
	require.Len(t, compatible, 1)
	require.Equal(t, -1, compatible[0].Stream.FileIndex)

	streams, err = GetStreamsForHashesWithCheckedFiles("series", "tt1234567:1:2", []string{missingFilesHash}, nsid, map[string][]store.MagnetFile{
		missingFilesHash: {{Path: "/Show.S01E02.mkv", Name: "Show.S01E02.mkv", Size: 1234, Idx: 9, Source: "rd"}},
	})
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Equal(t, 9, streams[0].R.File.Idx)
	require.Equal(t, "Show.S01E02.mkv", streams[0].R.File.Name)
	require.Equal(t, "1.2 KB", streams[0].R.File.Size)
	require.Equal(t, 9, streams[0].Stream.FileIndex)
	require.Equal(t, int64(1234), streams[0].Stream.BehaviorHints.VideoSize)
}

func TestCheckedOpaqueVideoPreservesSeriesCandidate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		files      []store.MagnetFile
		want       int
		index      int
		candidates int
	}{
		{"opaque video", []store.MagnetFile{{Name: "video.mkv", Idx: 7}}, 1, -1, 1},
		{"opaque video with subtitle", []store.MagnetFile{{Name: "video.mkv"}, {Name: "subtitle.srt"}}, 1, -1, 1},
		{"matching episode", []store.MagnetFile{{Name: "Show.S01E02.mkv", Idx: 7}}, 1, 7, 1},
		{"wrong episode", []store.MagnetFile{{Name: "Show.S01E03.mkv"}}, 0, 0, 0},
		{"wrong season", []store.MagnetFile{{Name: "Show.S02.mkv"}}, 0, 0, 0},
		{"opaque bundle", []store.MagnetFile{{Name: "video.mkv"}, {Name: "other.mkv"}}, 0, 0, 0},
		{"nonvideo", []store.MagnetFile{{Name: "readme.txt"}}, 0, 0, 1},
		{"blocked companion", []store.MagnetFile{{Name: "video.mkv"}, {Name: "setup.exe"}}, 0, 0, 0},
		{"matching episode with blocked companion", []store.MagnetFile{{Name: "Show.S01E02.mkv"}, {Name: "setup.exe"}}, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.SetupDB(t)
			previous := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = false
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
			hash, sid := testutil.Hash(t), "tt1234567:1:2"
			_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Show.S01E02.1080p", "tb", "Show.S01E02.1080p")
			require.NoError(t, err)
			nsid, err := torrent_stream.NormalizeStreamId(sid)
			require.NoError(t, err)
			before, err := GetStreamsForHashes("series", sid, []string{hash}, nsid)
			require.NoError(t, err)
			require.Len(t, before, 1)
			after, err := GetStreamsForHashesWithCheckedFiles("series", sid, []string{hash}, nsid, map[string][]store.MagnetFile{hash: tc.files})
			require.NoError(t, err)
			require.Len(t, after, tc.want)
			if tc.want > 0 {
				require.Equal(t, tc.index, after[0].Stream.FileIndex)
			}
			// Provider tracking can finish before the candidate is rebuilt.
			// Reading that same list from storage must not change the result.
			var recorded []torrent_stream.InsertData
			for _, f := range tc.files {
				recorded = append(recorded, torrent_stream.InsertData{Hash: hash, File: torrent_stream.File{Name: f.Name, Path: "/" + f.Name, Idx: f.Idx}})
			}
			require.NoError(t, torrent_stream.Record(recorded, false))
			// A later request starts without checked files. The candidate must
			// survive that stage before it can be checked and rebuilt again.
			candidates, err := GetStreamCandidatesForHashes("series", sid, []string{hash}, nsid)
			require.NoError(t, err)
			require.Len(t, candidates, tc.candidates)
			if tc.want > 0 {
				require.Equal(t, tc.index == -1, candidates[0].FileListDeferred)
			}
			after, err = GetStreamsForHashesWithCheckedFiles("series", sid, []string{hash}, nsid, map[string][]store.MagnetFile{hash: tc.files})
			require.NoError(t, err)
			require.Len(t, after, tc.want)
			if tc.want > 0 {
				require.Equal(t, tc.index, after[0].Stream.FileIndex)
			}
		})
	}
}

func TestCheckedFilesResolveStoredOpaqueVideo(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files       []store.MagnetFile
		want, index int
	}{
		{"still opaque", []store.MagnetFile{{Name: "video.mkv", Idx: 9}}, 1, -1},
		{"matching episode", []store.MagnetFile{{Name: "Show.S01E02.mkv", Idx: 9, Size: 1234}}, 1, 9},
		{"wrong episode", []store.MagnetFile{{Name: "Show.S01E03.mkv"}}, 0, 0},
		{"wrong season", []store.MagnetFile{{Name: "Show.S02E02.mkv"}}, 0, 0},
		{"opaque bundle", []store.MagnetFile{{Name: "video.mkv"}, {Name: "other.mkv"}}, 0, 0},
		{"nonvideo", []store.MagnetFile{{Name: "readme.txt"}}, 0, 0},
		{"blocked companion", []store.MagnetFile{{Name: "Show.S01E02.mkv"}, {Name: "setup.exe"}}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.SetupDB(t)
			previous := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = false
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
			hash, sid := testutil.Hash(t), "tt1234567:1:2"
			_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Show.S01E02.1080p", "tb", "Show.S01E02.1080p")
			require.NoError(t, err)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/video.mkv", Idx: 7}}}, false))
			nsid, err := torrent_stream.NormalizeStreamId(sid)
			require.NoError(t, err)
			candidates, err := GetStreamCandidatesForHashes("series", sid, []string{hash}, nsid)
			require.NoError(t, err)
			require.Len(t, candidates, 1)
			require.True(t, candidates[0].FileListDeferred)
			streams, err := GetStreamsForHashesWithCheckedFiles("series", sid, []string{hash}, nsid, map[string][]store.MagnetFile{hash: tc.files})
			require.NoError(t, err)
			require.Len(t, streams, tc.want)
			if tc.want > 0 {
				require.Equal(t, tc.index, streams[0].Stream.FileIndex)
				if tc.index != -1 {
					require.Equal(t, tc.files[0].Name, streams[0].Stream.BehaviorHints.Filename)
					require.Equal(t, tc.files[0].Size, streams[0].Stream.BehaviorHints.VideoSize)
				}
			}
			// Fresh clean files cannot erase retained strict blocked evidence.
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/setup.exe"}}}, false))
			streams, err = GetStreamsForHashesWithCheckedFiles("series", sid, []string{hash}, nsid, map[string][]store.MagnetFile{hash: tc.files})
			require.NoError(t, err)
			require.Empty(t, streams)
		})
	}
}
