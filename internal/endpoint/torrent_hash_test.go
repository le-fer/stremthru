package endpoint

import (
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/stretchr/testify/require"
)

func TestRecordPeerTorrentsNormalizesHashAndRetainsBlockedEvidence(t *testing.T) {
	testutil.SetupDB(t)
	hash := testutil.Hash(t)
	payload := &RecordTorrentsPayload{Items: []torrent_info.TorrentItem{{
		Hash: strings.ToUpper(hash), TorrentTitle: "Movie.2024.1080p",
		Source: torrent_info.TorrentInfoSourceTorBox,
		Files:  ts.Files{{Name: "Setup.EXE", Path: "/Setup.EXE"}},
	}}}
	// Execute the handler's recording operation synchronously so all writes and
	// cache updates finish before the fixture restores shared database state.
	require.NoError(t, recordTorrents(payload))
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ? AND p = ?", hash, "/Setup.EXE").Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", strings.ToUpper(hash)).Scan(&count))
	require.Zero(t, count)
	require.Equal(t, "Setup.EXE", payload.Items[0].Files[0].Name)
}
