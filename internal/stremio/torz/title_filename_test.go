package stremio_torz

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	"github.com/MunifTanjim/stremthru/internal/logger"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tznc "github.com/MunifTanjim/stremthru/internal/torznab/client"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestStoredTitleFilenameRequiresAllowedVideoExtension(t *testing.T) {
	for _, tc := range []struct {
		name, title, blocked, want string
	}{
		{"allowed video", "Movie.2024.1080p.mkv", "", "Movie.2024.1080p.mkv"},
		{"blocked video", "Movie.2024.1080p.mkv", "+mkv", ""},
		{"nonvideo", "Movie.2024.1080p.txt", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.SetupDB(t)
			old := util.FileExtDisallowed
			t.Cleanup(func() { util.FileExtDisallowed = old })
			var err error
			util.FileExtDisallowed, err = util.ParseDisallowedExtensions(tc.blocked)
			require.NoError(t, err)
			hash := testutil.Hash(t)
			_, err = db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, tc.title, "tb", tc.title)
			require.NoError(t, err)
			nsid, err := torrent_stream.NormalizeStreamId("tt1234567")
			require.NoError(t, err)
			for _, checked := range []map[string][]store.MagnetFile{nil, {hash: {{Name: "allowed.mp4", Path: "/allowed.mp4"}}}} {
				streams, err := GetStreamsForHashesWithCheckedFiles("movie", "tt1234567", []string{hash}, nsid, checked)
				require.NoError(t, err)
				require.Len(t, streams, 1, "missing or allowed file lists remain compatible")
				require.Equal(t, tc.want, streams[0].R.File.Name)
				require.Equal(t, tc.want, streams[0].BehaviorHints.Filename)
			}
		})
	}
}

func TestIndexerTitleFilenameRequiresAllowedVideoExtension(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new torrent"
		if existing {
			name = "existing torrent"
		}
		t.Run(name, func(t *testing.T) {
			testutil.SetupDB(t)
			old := util.FileExtDisallowed
			t.Cleanup(func() { util.FileExtDisallowed = old })
			var err error
			util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
			require.NoError(t, err)
			sid, hash, title := "tt1234567", testutil.Hash(t), "Movie.2024.1080p.mkv"
			require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: sid, Title: "Movie", Year: 2024, Type: "movie"}}))
			if existing {
				_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, title, "tb", title)
				require.NoError(t, err)
			}
			client := tznc.NewClient(&tznc.ClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<caps><searching><movie-search available="yes" supportedParams="imdbid"/></searching></caps>`)), Request: r}, nil
			})}})
			item := tznc.Torz{Hash: hash, MagnetLink: "magnet:?xt=urn:btih:" + hash, Title: title, Indexer: "fixture", Files: []torrent_stream.File{{Name: "allowed.mp4", Path: "/allowed.mp4"}}}
			ctx := &Ctx{Ctx: stremio_shared.Ctx{Log: logger.Scoped("test")}, Indexers: []tznc.Indexer{&fileEvidenceTestIndexer{Client: client, items: []tznc.Torz{item}}}}
			var recorded func() bool
			if !existing {
				recorded = torrent_stream.ObserveFileRecordingForTesting(hash, []string{"/allowed.mp4"})
			}
			result, err := getStreamsFromIndexers(context.Background(), ctx, "movie", sid)
			if recorded != nil {
				require.Eventually(t, recorded, 5*time.Second, 10*time.Millisecond)
			}
			require.NoError(t, err)
			require.Len(t, result.Streams, 1)
			require.Empty(t, result.Streams[0].R.File.Name, "a blocked title must not become a requested playback filename")
		})
	}
}
