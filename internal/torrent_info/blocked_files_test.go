package torrent_info

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/stretchr/testify/require"
)

func TestUpsertRetainsDisallowedFileEvidence(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for i, tc := range []struct {
			name  string
			files ts.Files
		}{
			{"executable only", ts.Files{{Path: "/setup.EXE", Name: "setup.EXE"}}},
			{"script only", ts.Files{{Path: "/setup.sh", Name: "setup.sh"}}},
			{"video and executable", ts.Files{{Path: "/movie.mkv", Name: "movie.mkv"}, {Path: "/setup.exe", Name: "setup.exe"}}},
			{"bundle path", ts.Files{{Path: "/Example.app", Name: "Example.app"}}},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.name, allow), func(t *testing.T) {
				previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
				setupTestDB(t)
				hash := fmt.Sprintf("%040x", i+100)
				item := TorrentInfoInsertData{Hash: hash, TorrentTitle: "Movie.2024.1080p", Source: TorrentInfoSourceTorBox, Files: tc.files}
				require.NoError(t, Upsert([]TorrentInfoInsertData{item}, TorrentInfoCategoryMovie, true))
				// Check SQL itself: a process-wide read cache must not satisfy this assertion.
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
				require.Equal(t, len(tc.files), count)
				files, err := ts.GetFilesByHashes([]string{hash})
				require.NoError(t, err)
				require.Len(t, files[hash], len(tc.files))
				require.True(t, files[hash].HasDisallowedFile())
				// A later unresolved result must not erase previously observed file evidence.
				item.Files = nil
				require.NoError(t, Upsert([]TorrentInfoInsertData{item}, TorrentInfoCategoryMovie, true))
				files, err = ts.GetFilesByHashes([]string{hash})
				require.NoError(t, err)
				require.True(t, files[hash].HasDisallowedFile())
			})
		}
	}
}

func TestUpsertPremiumizePreservesPathBasedStorageRestriction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files ts.Files
		count int
	}{
		{"video path retains mixed evidence", ts.Files{{Name: "readme.txt", Path: "/movie.mkv"}, {Name: "setup.exe", Path: "/setup.exe"}}, 2},
		{"video name without video path remains excluded", ts.Files{{Name: "movie.mkv", Path: "/readme.txt"}, {Name: "setup.exe", Path: "/setup.exe"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupTestDB(t)
			hash := strings.Repeat("c", 40)
			require.NoError(t, Upsert([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Movie.2024.1080p", Source: TorrentInfoSourcePremiumize, Files: tc.files}}, TorrentInfoCategoryMovie, true))
			var count int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
			require.Equal(t, tc.count, count)
		})
	}
}

func TestUpsertRetainsEvidenceForRepeatedHash(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			setupTestDB(t)
			previous := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = allow
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
			hash := strings.Repeat("b", 40)
			items := []TorrentInfoInsertData{
				{Hash: hash, TorrentTitle: "Movie.2024.1080p", Source: TorrentInfoSourceIndexer, Files: ts.Files{{Path: "/readme.txt"}}},
				{Hash: hash, TorrentTitle: "Other.Movie.2025.1080p", Source: TorrentInfoSourceIndexer, Files: ts.Files{{Path: "/setup.exe"}}},
				{Hash: hash, TorrentTitle: "Untrusted", Source: TorrentInfoSourceTorrin, Files: ts.Files{{Path: "/untrusted.exe"}}},
				{Hash: hash, TorrentTitle: "Offcloud", Source: TorrentInfoSourceOffcloud, Files: ts.Files{{Path: "/offcloud.exe"}}},
			}
			require.NoError(t, Upsert(items, TorrentInfoCategoryMovie, true))
			var count int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
			require.Equal(t, 2, count, "retain both trusted lists and preserve source exclusions")
			var title string
			require.NoError(t, db.QueryRow("SELECT t_title FROM torrent_info WHERE hash = ?", hash).Scan(&title))
			require.Equal(t, items[0].TorrentTitle, title, "torrent metadata remains deduplicated")
			files, err := ts.GetFilesByHashes([]string{hash})
			require.NoError(t, err)
			require.True(t, files[hash].HasDisallowedFile())
		})
	}
}
