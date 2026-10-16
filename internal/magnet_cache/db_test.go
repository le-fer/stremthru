package magnet_cache

import (
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/stretchr/testify/require"
)

func TestNormalizeFilesByHashReusesCanonicalFileSlice(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	files := torrent_stream.Files{{Path: "/movie.mkv"}}
	input := map[string]torrent_stream.Files{hash: files}

	normalized := normalizeFilesByHash(input)
	require.Same(t, &files[0], &normalized[hash][0])
}

func TestNormalizeFilesByHashMergesCanonicalCollisionsWithoutMutatingInput(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	canonicalFiles := make(torrent_stream.Files, 1, 2)
	canonicalFiles[0] = torrent_stream.File{Path: "/movie.mkv"}
	uppercaseFiles := torrent_stream.Files{{Path: "/setup.exe"}}
	input := map[string]torrent_stream.Files{
		hash:                  canonicalFiles,
		strings.ToUpper(hash): uppercaseFiles,
		"invalid":             {{Path: "/ignored.mkv"}},
	}

	normalized := normalizeFilesByHash(input)
	require.ElementsMatch(t, torrent_stream.Files{{Path: "/movie.mkv"}, {Path: "/setup.exe"}}, normalized[hash])
	require.Len(t, normalized, 1)
	require.Equal(t, torrent_stream.File{}, canonicalFiles[:cap(canonicalFiles)][1])
}

func TestNormalizeCachedByHashMergesCanonicalCollisions(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	normalized := normalizeCachedByHash(map[string]bool{
		hash:                  false,
		strings.ToUpper(hash): true,
		"invalid":             true,
	})

	require.Equal(t, map[string]bool{hash: true}, normalized)
	require.NotNil(t, normalizeCachedByHash(nil))
}

func TestNormalizeHashMapsRejectEmptyKeys(t *testing.T) {
	for _, includeValid := range []bool{false, true} {
		name := "empty only"
		if includeValid {
			name = "empty alongside canonical"
		}
		t.Run(name, func(t *testing.T) {
			hash := strings.Repeat("ab", 20)
			files := torrent_stream.Files{{Path: "/movie.mkv"}}
			filesByHash := map[string]torrent_stream.Files{"": files}
			cached := map[string]bool{"": true}
			wantFiles := map[string]torrent_stream.Files{}
			wantCached := map[string]bool{}
			if includeValid {
				filesByHash[hash], wantFiles[hash] = files, files
				cached[hash], wantCached[hash] = false, false
			}
			require.Equal(t, wantFiles, normalizeFilesByHash(filesByHash))
			require.Equal(t, wantCached, normalizeCachedByHash(cached))
			require.Contains(t, filesByHash, "", "normalization must not mutate input")
			require.Contains(t, cached, "", "normalization must not mutate input")
		})
	}
}
