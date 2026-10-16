package torrent_stream_test

import (
	"encoding/base32"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream/media_info"
	"github.com/stretchr/testify/require"
)

func base32Hash(t *testing.T, hash string) string {
	t.Helper()
	decoded, err := hex.DecodeString(hash)
	require.NoError(t, err)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded)
}

func TestHashReadsAndUpdatesCanonicalizeInputs(t *testing.T) {
	testutil.SetupDB(t)
	hash := testutil.Hash(t)
	path := "/movie.mkv"
	require.NoError(t, ts.Record([]ts.InsertData{{Hash: hash, File: ts.File{Path: path}}}, false))

	uppercase := strings.ToUpper(hash)
	encoded := base32Hash(t, hash)
	files, err := ts.GetFilesByHashes([]string{uppercase, encoded})
	require.NoError(t, err)
	require.Len(t, files[hash], 1)

	ts.TagStremId(uppercase, path, "tt1234567")
	file, err := ts.GetFile(encoded, "tt1234567")
	require.NoError(t, err)
	require.NotNil(t, file)
	require.Equal(t, path, file.Path)

	require.NoError(t, ts.SetMediaInfo(encoded, path, &media_info.MediaInfo{}))
	require.NotNil(t, ts.GetMediaInfo(uppercase, path))

	ids, err := ts.GetStremIdByHashes([]string{encoded})
	require.NoError(t, err)
	require.Equal(t, []string{"tt1234567"}, (*ids)[hash])
}
