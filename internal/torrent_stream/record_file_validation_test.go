package torrent_stream_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/stretchr/testify/require"
)

func TestRecordCanonicalizesAndValidatesHashes(t *testing.T) {
	testutil.SetupDB(t)
	hash := testutil.Hash(t)
	items := []ts.InsertData{
		{Hash: strings.ToUpper(hash), File: ts.File{Path: "/movie.mkv"}},
		{Hash: strings.Repeat("z", 40), File: ts.File{Path: "/invalid.mkv"}},
	}
	require.NoError(t, ts.Record(items, false))

	var canonical, uppercase, invalid int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&canonical))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", strings.ToUpper(hash)).Scan(&uppercase))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", strings.Repeat("z", 40)).Scan(&invalid))
	require.Equal(t, 1, canonical)
	require.Zero(t, uppercase)
	require.Zero(t, invalid)
}

func TestRecordSkipsBlockedNamesWithoutBlockedPaths(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, tc := range []struct {
			name        string
			adjustments string
			files       ts.Files
			paths       []string
		}{
			{"conflicting file only", "", ts.Files{{Name: "setup.EXE", Path: "/movie.mkv"}}, nil},
			{"mixed list", "", ts.Files{{Name: "setup.exe", Path: "/fake.mkv"}, {Name: "movie.mkv", Path: "/movie.mkv"}, {Name: "setup.exe", Path: "/setup.exe"}}, []string{"/movie.mkv", "/setup.exe"}},
			{"blocked path with video name", "", ts.Files{{Name: "movie.mkv", Path: "/setup.EXE"}}, []string{"/setup.EXE"}},
			{"missing name", "", ts.Files{{Path: "/movie.mkv"}}, []string{"/movie.mkv"}},
			{"unblocked disagreement", "", ts.Files{{Name: "readme.txt", Path: "/movie.mkv"}}, []string{"/movie.mkv"}},
			{"configured blocked video name", "+mkv", ts.Files{{Name: "movie.mkv", Path: "/movie.mp4"}}, nil},
			{"removed blocked extension", "-exe", ts.Files{{Name: "setup.exe", Path: "/movie.mkv"}}, []string{"/movie.mkv"}},
			{"valid sibling shares path", "", ts.Files{{Name: "setup.exe", Path: "/movie.mkv"}, {Name: "movie.mkv", Path: "/movie.mkv"}}, []string{"/movie.mkv"}},
			{"relative path does not fail valid sibling", "", ts.Files{{Name: "readme.txt", Path: "readme.txt"}, {Name: "movie.mkv", Path: "/movie.mkv"}}, []string{"/movie.mkv"}},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.name, allow), func(t *testing.T) {
				testutil.SetupDB(t)
				previousAllow, previousExtensions := config.AllowVideoTorrentsWithDisallowedFiles, util.FileExtDisallowed
				t.Cleanup(func() {
					config.AllowVideoTorrentsWithDisallowedFiles, util.FileExtDisallowed = previousAllow, previousExtensions
				})
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				var err error
				util.FileExtDisallowed, err = util.ParseDisallowedExtensions(tc.adjustments)
				require.NoError(t, err)
				hash := testutil.Hash(t)
				items := make([]ts.InsertData, len(tc.files))
				for i, file := range tc.files {
					items[i] = ts.InsertData{Hash: hash, File: file}
				}
				original := slices.Clone(items)
				require.NoError(t, ts.Record(items, false))
				require.Equal(t, original, items, "recording must not rewrite the supplied evidence")
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
				require.Equal(t, len(tc.paths), count)
				stored, err := ts.GetFilesByHashes([]string{hash})
				require.NoError(t, err)
				paths := make([]string, 0, len(stored[hash]))
				for _, file := range stored[hash] {
					paths = append(paths, file.Path)
				}
				require.ElementsMatch(t, tc.paths, paths)
			})
		}
	}
}
