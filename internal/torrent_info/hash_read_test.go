package torrent_info

import (
	"encoding/base32"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func base32Hash(t *testing.T, hash string) string {
	t.Helper()
	decoded, err := hex.DecodeString(hash)
	require.NoError(t, err)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded)
}

func TestHashReadsCanonicalizeInputs(t *testing.T) {
	setupTestDB(t)
	hash := strings.Repeat("ab", 20)
	require.NoError(t, Upsert([]TorrentInfoInsertData{{
		Hash: hash, TorrentTitle: "Movie.2024.1080p", Size: 123, Source: TorrentInfoSourceTorBox,
	}}, TorrentInfoCategoryMovie, false))

	for _, input := range []string{strings.ToUpper(hash), base32Hash(t, hash)} {
		t.Run(input, func(t *testing.T) {
			info, err := GetByHash(input)
			require.NoError(t, err)
			require.NotNil(t, info)
			require.Equal(t, hash, info.Hash)

			infos, err := GetByHashes([]string{input})
			require.NoError(t, err)
			require.Contains(t, infos, hash)

			exists, err := ExistsByHash([]string{input})
			require.NoError(t, err)
			require.True(t, exists[hash])

			basic, err := GetBasicInfoByHash([]string{input})
			require.NoError(t, err)
			require.Equal(t, "Movie.2024.1080p", basic[hash].TorrentTitle)
		})
	}
}
