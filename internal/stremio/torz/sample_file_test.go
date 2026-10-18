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
	"time"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	"github.com/MunifTanjim/stremthru/internal/logger"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tznc "github.com/MunifTanjim/stremthru/internal/torznab/client"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestStreamsExcludeSamplesFromSelectionAndDisplay(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
	const sid = "tt9990001:2:3"
	const sampleName = "example.series.s02e03.1080p.web.h264-group-sample.mkv"
	const fullName = "example.series.s02e03.1080p.web.h264-group.mkv"
	for _, tc := range []struct {
		name                      string
		mapped, sampleOnly, movie bool
	}{
		{name: "inferred episode"}, {name: "previously mapped sample", mapped: true}, {name: "sample only", sampleOnly: true}, {name: "movie sample only", movie: true, sampleOnly: true}, {name: "movie with full video", movie: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
			files := torrent_stream.Files{{Name: sampleName, Path: "/Example.Series.S02E03/Sample/" + sampleName, Idx: 0, Size: 100}}
			if tc.mapped {
				files[0].SId = sid
			}
			if !tc.sampleOnly {
				files = append(files, torrent_stream.File{Name: fullName, Path: "/Example.Series.S02E03/" + fullName, Idx: 1, Size: 1000})
			}
			files = append(files, torrent_stream.File{Name: "example.series.s02e03.nfo", Path: "/Example.Series.S02E03/example.series.s02e03.nfo"})
			require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Example.Series.S02E03.1080p.WEB.H264-GROUP", Source: torrent_info.TorrentInfoSourceTorrentio, Files: files}}, "", false))
			request, category := sid, "series"
			if tc.movie {
				request, category = "tt9990001", "movie"
			}
			nsid, err := torrent_stream.NormalizeStreamId(request)
			require.NoError(t, err)
			streams, err := GetStreamsForHashes(category, request, []string{hash}, nsid)
			require.NoError(t, err)
			if tc.sampleOnly {
				require.Empty(t, streams)
				return
			}
			require.Len(t, streams, 1)
			require.Equal(t, fullName, streams[0].R.File.Name)
			require.Equal(t, fullName, streams[0].BehaviorHints.Filename)
			require.Equal(t, 1, streams[0].FileIndex)
			rendered, err := streamTemplate.Execute(streams[0].Stream, streams[0].R)
			require.NoError(t, err)
			require.False(t, strings.Contains(rendered.Description, sampleName))
		})
	}
}

// Fileless live results must not reintroduce sample-only stored torrents.
type sampleTestIndexer struct {
	*tznc.Client
	items []tznc.Torz
}

func (i *sampleTestIndexer) GetId() string   { return "sample-fixture" }
func (i *sampleTestIndexer) GetName() string { return "sample-fixture" }
func (i *sampleTestIndexer) Search(context.Context, url.Values) ([]tznc.Torz, error) {
	return i.items, nil
}

type sampleTestTransport func(*http.Request) (*http.Response, error)

func (f sampleTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLiveIndexerExcludesSampleOnlyResults(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
	require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: "tt9990001", Title: "Example Series", Type: "tvSeries"}}))
	const title = "Example.Series.S02E03.1080p"
	sample := torrent_stream.File{Name: "Example.Series.S02E03-sample.mkv", Path: "/Sample/Example.Series.S02E03-sample.mkv", SId: "tt9990001:2:3"}
	full := torrent_stream.File{Name: "Example.Series.S02E03.mkv", Path: "/Example.Series.S02E03.mkv", Idx: 7, SId: "tt9990001:2:3"}
	for _, tc := range []struct {
		name, title, storedTitle string
		live, stored             torrent_stream.Files
		retainsRejection         bool
		want                     int
	}{
		{name: "stored samples and fileless live result", title: title, storedTitle: title, stored: torrent_stream.Files{sample}},
		{name: "fileless sample release", title: sample.Name, storedTitle: title},
		{name: "stored sample filename", title: title, storedTitle: sample.Name},
		{name: "live sample only", title: title, storedTitle: title, live: torrent_stream.Files{sample}, retainsRejection: true},
		{name: "stored full file remains usable", title: title, storedTitle: title, live: torrent_stream.Files{sample}, stored: torrent_stream.Files{full}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
			require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{Hash: hash, TorrentTitle: tc.storedTitle, Source: torrent_info.TorrentInfoSourceTorrentio, Files: tc.stored}}, "", false))
			nsid, err := torrent_stream.NormalizeStreamId("tt9990001:2:3")
			require.NoError(t, err)
			if tc.retainsRejection {
				before, err := GetStreamsForHashes("series", "tt9990001:2:3", []string{hash}, nsid)
				require.NoError(t, err)
				require.Len(t, before, 1, "the stored fileless release initially supplies season evidence")
			}
			client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: sampleTestTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><tv-search available="yes" supportedParams="imdbid,season,ep"/></searching></caps>`)), Request: r}, nil
			})}})
			indexer := &sampleTestIndexer{Client: client, items: []tznc.Torz{{Hash: hash, Title: tc.title, MagnetLink: "magnet:?xt=urn:btih:" + hash, Files: tc.live}}}
			ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("sample-test")}, Indexers: []tznc.Indexer{indexer}}
			streams, _, err := GetStreamsFromIndexers(context.Background(), ctx, "series", "tt9990001:2:3")
			require.NoError(t, err)
			require.Len(t, streams, tc.want)
			if tc.want > 0 {
				require.Equal(t, full.Name, streams[0].R.File.Name)
			}
			if tc.retainsRejection {
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ? AND p = ?", hash, sample.Path).Scan(&count))
				require.Equal(t, 1, count, "sample-only indexer evidence must be retained before returning")
				after, err := GetStreamsForHashes("series", "tt9990001:2:3", []string{hash}, nsid)
				require.NoError(t, err)
				require.Empty(t, after, "later stored requests must remember sample-only rejection without the indexer")
			}
		})
	}
}

func TestFreshIndexerNeverDefaultsToSampleIndex(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
	require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: "tt9990001", Title: "Example Series", Type: "tvSeries"}}))
	for _, tc := range []struct {
		name, filename string
		wantIndex      int
	}{
		{"matching full episode", "Example.Series.S02E03.mkv", 1},
		{"unverified episode index remains unknown", "Example.Series.S02E04.mkv", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
			files := torrent_stream.Files{
				{Name: "Example.Series.S02E03-sample.mkv", Path: "/Sample/Example.Series.S02E03-sample.mkv", Idx: 0, Size: 5000},
				{Name: tc.filename, Path: "/" + tc.filename, Idx: 1, Size: 1000},
				{Name: "release.nfo", Path: "/release.nfo", Idx: 2},
			}
			client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: sampleTestTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><tv-search available="yes" supportedParams="imdbid,season,ep"/></searching></caps>`)), Request: r}, nil
			})}})
			indexer := &sampleTestIndexer{Client: client, items: []tznc.Torz{{Hash: hash, Title: "Example.Series.S02.1080p", MagnetLink: "magnet:?xt=urn:btih:" + hash, Files: files}}}
			ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("sample-test")}, Indexers: []tznc.Indexer{indexer}}
			streams, _, err := GetStreamsFromIndexers(context.Background(), ctx, "series", "tt9990001:2:3")
			require.NoError(t, err)
			require.Len(t, streams, 1)
			require.Equal(t, tc.wantIndex, streams[0].FileIndex)
			require.Equal(t, tc.wantIndex, streams[0].R.File.Idx)
			if tc.wantIndex >= 0 {
				require.Equal(t, tc.filename, streams[0].BehaviorHints.Filename)
			}
			// Join asynchronous metadata recording before restoring the fixture database.
			require.Eventually(t, func() bool {
				var count int
				return db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count) == nil && count == len(files)
			}, time.Second, 10*time.Millisecond)
		})
	}
}
