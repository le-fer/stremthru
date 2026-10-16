package stremio_torz

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	"github.com/MunifTanjim/stremthru/internal/logger"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tznc "github.com/MunifTanjim/stremthru/internal/torznab/client"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

type fileEvidenceTestIndexer struct {
	*tznc.Client
	items []tznc.Torz
}

func TestIndexerDisallowedEvidenceRejectsEveryEntryForHash(t *testing.T) {
	for _, policy := range []struct{ allow, storedVideo bool }{{false, false}, {false, true}, {true, true}} {
		allow := policy.allow
		for _, blockedFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("allow=%t/storedVideo=%t/blockedFirst=%t", allow, policy.storedVideo, blockedFirst), func(t *testing.T) {
				testutil.SetupDB(t)
				previous := config.AllowVideoTorrentsWithDisallowedFiles
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
				sid, hash := "tt1234567", testutil.Hash(t)
				require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: sid, Title: "Movie", Year: 2024, Type: "movie"}}))
				if policy.storedVideo {
					// Reuse existing video metadata so this acceptance fixture has no pending upsert.
					require.NoError(t, torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{
						Hash: hash, TorrentTitle: "Movie.2024.1080p", Source: torrent_info.TorrentInfoSourceIndexer,
						Files: torrent_stream.Files{{Name: "movie.mkv", Path: "/movie.mkv", SId: sid}},
					}}, torrent_info.TorrentInfoCategoryUnknown, false))
				}
				// Verify that indexer rejection also removes an existing stored candidate.
				nsid, err := torrent_stream.NormalizeStreamId(sid)
				require.NoError(t, err)
				stored, err := GetStreamsForHashes("movie", sid, []string{hash}, nsid)
				require.NoError(t, err)
				if policy.storedVideo {
					require.Len(t, stored, 1)
				}
				client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><movie-search available="yes" supportedParams="imdbid"/></searching></caps>`)), Request: r}, nil
				})}})
				entry := func(name string) tznc.Torz {
					return tznc.Torz{Hash: hash, MagnetLink: "magnet:?xt=urn:btih:" + hash, Title: "Movie.2024.1080p", Indexer: "fixture", Files: []torrent_stream.File{{Name: name, Path: "/" + name}}}
				}
				items := []tznc.Torz{entry("movie.mkv"), entry("setup.exe")}
				if blockedFirst {
					items[0], items[1] = items[1], items[0]
				}
				ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("test")}, Indexers: []tznc.Indexer{&fileEvidenceTestIndexer{Client: client, items: items}}}
				result, err := getStreamsFromIndexers(context.Background(), ctx, "movie", sid)
				require.NoError(t, err)
				if allow {
					require.Len(t, result.Streams, 1)
					require.Equal(t, hash, result.Streams[0].R.Hash)
					require.Empty(t, result.InvalidByHash)
					require.NotEmpty(t, mergeIndexerStreams(stored, result, nil))
				} else {
					require.Empty(t, result.Streams)
					require.Contains(t, result.InvalidByHash, hash)
					require.Empty(t, mergeIndexerStreams(stored, result, map[string][]store.MagnetFile{
						hash: {{Name: "movie.mkv", Path: "/movie.mkv"}},
					}), "strict disallowed evidence must not be cleared by checked video files")
				}
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
				require.Equal(t, 2, count, "both entries' evidence must remain available")
			})
		}
	}
}

func (i *fileEvidenceTestIndexer) GetId() string   { return "fixture" }
func (i *fileEvidenceTestIndexer) GetName() string { return "fixture" }
func (i *fileEvidenceTestIndexer) Search(context.Context, url.Values) ([]tznc.Torz, error) {
	return i.items, nil
}

func TestIndexerRetainsRepeatedHashFilesBeforeExcludingResults(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			testutil.SetupDB(t)
			old := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = allow
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = old })
			sid, hash := "tt1234567", testutil.Hash(t)
			require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: sid, Title: "Movie", Year: 2024, Type: "movie"}}))
			client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><movie-search available="yes" supportedParams="imdbid"/></searching></caps>`)), Request: r}, nil
			})}})
			indexer := &fileEvidenceTestIndexer{Client: client, items: []tznc.Torz{
				{Hash: hash, MagnetLink: "magnet:?xt=urn:btih:" + hash, Title: "Movie.2024.1080p", Indexer: "fixture", Files: []torrent_stream.File{{Name: "readme.txt", Path: "/readme.txt"}}},
				{Hash: hash, MagnetLink: "magnet:?xt=urn:btih:" + hash, Title: "Movie.2024.1080p", Indexer: "fixture", Files: []torrent_stream.File{{Name: "setup.exe", Path: "/setup.exe"}}},
			}}
			ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("test")}, Indexers: []tznc.Indexer{indexer}}
			result, err := getStreamsFromIndexers(context.Background(), ctx, "movie", sid)
			require.NoError(t, err)
			require.Empty(t, result.Streams)
			var count int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ? AND p = ?", hash, "/setup.exe").Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
			require.Equal(t, 2, count)
		})
	}
}

func TestIndexerMissingFileListDoesNotClearNonvideoRejection(t *testing.T) {
	testutil.SetupDB(t)
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	config.AllowVideoTorrentsWithDisallowedFiles = false
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })

	sid, hash := "tt1234567", testutil.Hash(t)
	require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: sid, Title: "Movie", Year: 2024, Type: "movie"}}))
	client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><movie-search available="yes" supportedParams="imdbid"/></searching></caps>`)), Request: r}, nil
	})}})
	base := tznc.Torz{Hash: hash, MagnetLink: "magnet:?xt=urn:btih:" + hash, Title: "Movie.2024.1080p", Indexer: "fixture"}
	withNonvideo := base
	withNonvideo.Files = []torrent_stream.File{{Name: "readme.txt", Path: "/readme.txt"}}
	ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("test")}, Indexers: []tznc.Indexer{&fileEvidenceTestIndexer{Client: client, items: []tznc.Torz{withNonvideo, base}}}}

	result, err := getStreamsFromIndexers(context.Background(), ctx, "movie", sid)
	require.NoError(t, err)
	require.Len(t, result.Streams, 1, "a missing file list remains compatible until validation")
	require.Contains(t, result.InvalidByHash, hash, "missing evidence must not clear a supplied nonvideo list")
	require.NotContains(t, result.ValidatedVideoByHash, hash)
	require.Empty(t, mergeIndexerStreams(nil, result, nil))
	require.Len(t, mergeIndexerStreams(nil, result, map[string][]store.MagnetFile{
		hash: {{Name: "movie.mkv", Path: "/movie.mkv"}},
	}), 1, "checked video files may clear current indexer no-video evidence")
}

func TestIndexerAllowedVideoListClearsNonvideoRejection(t *testing.T) {
	testutil.SetupDB(t)
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	config.AllowVideoTorrentsWithDisallowedFiles = false
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })

	sid, hash := "tt1234567", testutil.Hash(t)
	require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: sid, Title: "Movie", Year: 2024, Type: "movie"}}))
	client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><movie-search available="yes" supportedParams="imdbid"/></searching></caps>`)), Request: r}, nil
	})}})
	entry := func(name string) tznc.Torz {
		return tznc.Torz{Hash: hash, MagnetLink: "magnet:?xt=urn:btih:" + hash, Title: "Movie.2024.1080p", Indexer: "fixture", Files: []torrent_stream.File{{Name: name, Path: "/" + name}}}
	}
	ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("test")}, Indexers: []tznc.Indexer{&fileEvidenceTestIndexer{Client: client, items: []tznc.Torz{entry("readme.txt"), entry("movie.mkv")}}}}
	recorded := torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/movie.mkv"})

	result, err := getStreamsFromIndexers(context.Background(), ctx, "movie", sid)
	require.NoError(t, err)
	require.Len(t, result.Streams, 1)
	require.NotContains(t, result.InvalidByHash, hash, "a supplied allowed-video list may clear nonvideo-only evidence")
	require.Contains(t, result.ValidatedVideoByHash, hash, "current indexer video evidence must survive asynchronous persistence")
	require.Len(t, mergeIndexerStreams(nil, result, nil), 1)
	require.Eventually(t, recorded, 5*time.Second, 10*time.Millisecond)
}
