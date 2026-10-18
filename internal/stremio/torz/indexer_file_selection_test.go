package stremio_torz

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tznc "github.com/MunifTanjim/stremthru/internal/torznab/client"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Capabilities are served entirely in memory; no indexer or provider is contacted.
type fileSelectionCapsTransport struct{}

func (fileSelectionCapsTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Request:    r,
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(`<caps><searching><search available="yes" supportedParams="q"/><tv-search available="yes" supportedParams="q,imdbid,season,ep"/></searching></caps>`)),
	}, nil
}

type fileSelectionIndexer struct {
	*tznc.Client
	result tznc.Torz
}

func (fileSelectionIndexer) GetId() string   { return "synthetic" }
func (fileSelectionIndexer) GetName() string { return "Synthetic" }
func (i fileSelectionIndexer) Search(context.Context, url.Values) ([]tznc.Torz, error) {
	return []tznc.Torz{i.result}, nil
}

func TestCachedIndexerPrefersFreshFilesForInference(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	db.SetForTesting(sqlDB, t.Cleanup)
	const sid = "tt9998101:1:2"
	freshVideo := torrent_stream.Files{{Path: "/S01E02.mkv", Name: "S01E02.mkv", Idx: 1}}
	storedVideo := torrent_stream.Files{{Path: "/Season 1/E02.mkv", Name: "E02.mkv", Idx: 9}}
	wrongSeason := torrent_stream.Files{{Path: "/Season 5/E02.mkv", Name: "E02.mkv", Idx: 9}}
	readme := torrent_stream.Files{{Path: "/readme.txt", Name: "readme.txt", Idx: 0}}
	cases := []struct {
		name          string
		stored, fresh torrent_stream.Files
		wantIndex     int
	}{
		{"stored readme cannot hide fresh video", readme, freshVideo, 1},
		{"stored wrong season cannot hide fresh video", wrongSeason, freshVideo, 1},
		{"stored explicit mapping wins", torrent_stream.Files{{Path: "/Season 5/E99.mkv", Name: "E99.mkv", Idx: 9, SId: sid}}, freshVideo, 9},
		{"no fresh files retains stored inference", storedVideo, nil, 9},
		{"no fresh files retains season rejection", wrongSeason, nil, -1},
		{"fresh wrong season cannot use stale matching file", storedVideo, wrongSeason, -1},
		{"fresh nonvideo list cannot use stale matching file", storedVideo, readme, -1},
	}
	for _, hasTitle := range []bool{false, true} {
		if hasTitle {
			require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: "tt9998101", Title: "Synthetic Series", Type: "tvSeries"}}))
		}
		t.Run(fmt.Sprintf("title metadata %v", hasTitle), func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
					require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Synthetic Series S01E02", Size: 1000, Source: torrent_info.TorrentInfoSourceTorrentio, Files: tc.stored}}, "", false))
					known, err := torrent_info.GetByHashes([]string{hash})
					require.NoError(t, err)
					require.Contains(t, known, hash)
					stored, err := torrent_stream.GetFilesByHashes([]string{hash})
					require.NoError(t, err)
					require.Len(t, stored[hash], len(tc.stored))
					client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://indexer.invalid", HTTPClient: &http.Client{Transport: fileSelectionCapsTransport{}}})
					indexer := fileSelectionIndexer{Client: client, result: tznc.Torz{Hash: hash, Title: "Synthetic Series S01E02", Size: 1000, MagnetLink: "magnet:?xt=urn:btih:" + hash, Files: tc.fresh}}
					ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: log}, Indexers: []tznc.Indexer{indexer}}
					streams, _, err := GetStreamsFromIndexers(context.Background(), ctx, "series", sid)
					require.NoError(t, err)
					if tc.wantIndex == -1 {
						require.Empty(t, streams)
						return
					}
					require.Len(t, streams, 1)
					require.Equal(t, tc.wantIndex, streams[0].FileIndex)
				})
			}
		})
	}
}
