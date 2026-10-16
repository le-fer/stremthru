package stremio_torz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	fixture "github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestCachedPlaybackRejectsRetainedBlockedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mixed     bool
		requested string
		blocked   bool
	}{
		{"strict companion", false, "movie.mkv", true},
		{"mixed video", true, "movie.mkv", false},
		{"mixed executable", true, "setup.exe", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture.SetupDB(t)
			old := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = tc.mixed
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = old })
			hash := fixture.Hash(t)
			req := httptest.NewRequest(http.MethodGet, "http://fixture.invalid/playback?sid=fixture", nil)
			req.SetPathValue("userData", util.Base64Encode(`{"stores":[{"c":"rd","t":"`+hash+`"}]}`))
			req.SetPathValue("magnetHash", hash)
			req.SetPathValue("fileIdx", "0")
			req.SetPathValue("fileName", tc.requested)
			req.SetPathValue("stremId", "fixture")
			req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("review"), ReqQuery: req.URL.Query()})
			ud, err := getUserData(req)
			require.NoError(t, err)
			ctx, err := ud.GetRequestContext(req)
			require.NoError(t, err)
			s := ud.GetStoreByCode("")
			code := s.Store.GetName().Code()
			key := strings.Join([]string{ctx.ClientIP, string(code), s.AuthToken, "fixture", hash, "0", tc.requested}, ":")
			stremLinkCache.Add(key, "https://fixture.invalid/cached-video")
			t.Cleanup(func() { stremLinkCache.Remove(key) })
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/movie.mkv"}}, {Hash: hash, File: torrent_stream.File{Path: "/setup.exe"}}}, false))
			res := httptest.NewRecorder()
			handleStrem(res, req)
			require.Equal(t, http.StatusFound, res.Code)
			if tc.blocked {
				require.NotEqual(t, "https://fixture.invalid/cached-video", res.Header().Get("Location"))
			} else {
				require.Equal(t, "https://fixture.invalid/cached-video", res.Header().Get("Location"))
			}
		})
	}
}
func TestFreshCachedFilesReplaceStoredNonvideo(t *testing.T) {
	for _, tc := range []struct {
		name, sid, file string
		blocked, want   bool
	}{
		{"movie", "tt1234567", "movie.mkv", false, true},
		{"matching episode", "tt1234567:1:2", "Series.S01E02.mkv", false, true},
		{"wrong episode", "tt1234567:1:2", "Series.S01E03.mkv", false, false},
		{"missing list", "tt1234567", "", false, false},
		{"retained blocked", "tt1234567", "movie.mkv", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture.SetupDB(t)
			old := config.AllowVideoTorrentsWithDisallowedFiles
			config.AllowVideoTorrentsWithDisallowedFiles = false
			t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = old })
			hash := fixture.Hash(t)
			_, err := db.Exec("INSERT INTO torrent_info (hash,t_title,src,parser_input) VALUES (?,?,?,?)", hash, "Movie.2024.1080p", "tb", "Movie.2024.1080p")
			require.NoError(t, err)
			path := "/readme.txt"
			if tc.blocked {
				path = "/setup.exe"
			}
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: path}}}, false))
			nsid, err := torrent_stream.NormalizeStreamId(tc.sid)
			require.NoError(t, err)
			checked := map[string][]store.MagnetFile{}
			if tc.file != "" {
				checked[hash] = []store.MagnetFile{{Name: tc.file, Path: "/" + tc.file}}
			}
			typ := "movie"
			if nsid.IsSeries() {
				typ = "series"
			}
			streams, err := getStreamsForHashes(typ, tc.sid, []string{hash}, nsid, storedStreamFileOptions{CheckedFiles: checked})
			require.NoError(t, err)
			if tc.want {
				require.Len(t, streams, 1)
			} else {
				require.Empty(t, streams)
			}
		})
	}
}
