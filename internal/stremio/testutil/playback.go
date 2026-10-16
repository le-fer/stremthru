package testutil

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/server"
	"github.com/MunifTanjim/stremthru/internal/shared"
	fixture "github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/stretchr/testify/require"
)

// CheckPlayback invokes the actual HTTP handler with provider responses that
// expose the complete file list at playback time, after AddMagnet's first check.
func CheckPlayback(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	for _, allow := range []bool{false, true} {
		for _, tc := range []struct {
			name        string
			paths       []string
			rejected    bool
			rejectMixed bool
			blockMkv    bool
		}{
			{"explicit blocked filename", []string{"/movie.mkv", "/setup.exe"}, true, true, false},
			{"explicit blocked index", []string{"/movie.mkv", "/setup.exe"}, true, true, false},
			{"blocked after video", []string{"/movie.mkv", "/setup.exe"}, true, false, false},
			{"blocked before video", []string{"/setup.PS1", "/movie.mkv"}, true, false, false},
			{"application bundle", []string{"/Example.app/Contents/movie.mkv"}, false, false, false},
			{"blocked video only", []string{"/movie.mkv"}, true, true, true},
			{"blocked video and allowed video", []string{"/movie.mkv", "/movie.mp4"}, true, false, true},
			{"bundle and allowed video", []string{"/Example.app/Contents/hidden.mkv", "/movie.mkv"}, false, false, false},
			{"retained blocked evidence", []string{"/movie.mkv"}, true, false, false},
			{"video and subtitle", []string{"/movie.mkv", "/subtitle.srt"}, false, false, false},
			{"empty file list", []string{}, true, true, false},
			{"non-video only", []string{"/readme.txt"}, true, true, false},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.name, allow), func(t *testing.T) {
				previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
				previousExtensions := util.FileExtDisallowed
				t.Cleanup(func() { util.FileExtDisallowed = previousExtensions })
				videoName := "movie.mkv"
				if tc.blockMkv {
					var err error
					util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
					require.NoError(t, err)
					videoName = "movie.mp4"
				}
				fixture.SetupDB(t)
				hash := fixture.Hash(t)
				if tc.name == "retained blocked evidence" {
					require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/setup.exe", Idx: 1}}}, false))
				}
				api := shared.GetStore("realdebrid").(*realdebrid.StoreClient).GetClient()
				previousClient, previousBuddy, previousPeer := api.HTTPClient, buddy.Buddy, buddy.Peer
				previousHasBuddy, previousHasPeer, previousPeerToken := config.HasBuddy, config.HasPeer, config.PeerAuthToken
				t.Cleanup(func() {
					api.HTTPClient, buddy.Buddy, buddy.Peer = previousClient, previousBuddy, previousPeer
					config.HasBuddy, config.HasPeer, config.PeerAuthToken = previousHasBuddy, previousHasPeer, previousPeerToken
				})
				buddyTracked := make(chan string, 1)
				peerTracked := make(chan string, 1)
				config.HasBuddy, config.HasPeer, config.PeerAuthToken = true, true, "fixture-token"
				buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					buddyTracked <- r.URL.Path
					return fixture.JSONResponse(r, 200, map[string]any{"data": map[string]any{}}), nil
				})}})
				buddy.Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					peerTracked <- r.URL.Path
					return fixture.JSONResponse(r, 200, map[string]any{"data": map[string]any{}}), nil
				})}})
				waitForTracking := func() {
					t.Helper()
					select {
					case path := <-buddyTracked:
						require.Equal(t, "/v0/store/magnet-cache/track", path)
					case <-time.After(5 * time.Second):
						t.Fatal("Buddy tracking did not complete")
					}
					select {
					case path := <-peerTracked:
						require.Equal(t, "/v0/store/magnets/check", path)
					case <-time.After(5 * time.Second):
						t.Fatal("peer tracking did not complete")
					}
				}
				infoCalls, linkCalls := 0, 0
				api.HTTPClient = &http.Client{Transport: fixture.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/torrents"):
						return fixture.JSONResponse(r, 200, []map[string]any{{"id": hash, "hash": hash, "filename": "Movie"}}), nil
					case strings.Contains(r.URL.Path, "/info/"):
						infoCalls++
						paths := []string{"/" + videoName}
						if infoCalls > 1 {
							paths = tc.paths
						}
						files := []map[string]any{}
						links := []string{}
						for i, path := range paths {
							files = append(files, map[string]any{"id": i + 1, "path": path, "bytes": 100, "selected": 1})
							links = append(links, "https://fixture.invalid/"+filepath.Base(path))
						}
						return fixture.JSONResponse(r, 200, map[string]any{"id": hash, "hash": hash, "filename": "Movie", "status": "downloaded", "files": files, "links": links}), nil
					case strings.HasSuffix(r.URL.Path, "/unrestrict/link"):
						linkCalls++
						require.NoError(t, r.ParseForm())
						require.True(t, strings.HasSuffix(r.Form.Get("link"), "/"+videoName), "only the allowed video should be selected")
						// Stop after link generation to avoid content proxy/media probing.
						return nil, errors.New("fixture link generation reached")
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						return nil, errors.New("unexpected fixture request")
					}
				})}
				req := httptest.NewRequest(http.MethodGet, "http://fixture.invalid/playback?sid=fixture", nil)
				req.SetPathValue("userData", util.Base64Encode(`{"stores":[{"c":"rd","t":"`+hash+`"}]}`))
				req.SetPathValue("magnetHash", hash)
				req.SetPathValue("fileIdx", "0")
				req.SetPathValue("fileName", videoName)
				if tc.name == "explicit blocked filename" {
					req.SetPathValue("fileName", "setup.exe")
				}
				if tc.name == "explicit blocked index" {
					req.SetPathValue("fileName", "")
					req.SetPathValue("fileIdx", "1")
				}
				req.SetPathValue("stremId", "fixture")
				req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("test"), ReqQuery: req.URL.Query()})
				response := httptest.NewRecorder()
				handler(response, req)
				require.Equal(t, http.StatusFound, response.Code)
				if tc.name == "explicit blocked filename" || (tc.name == "retained blocked evidence" && !allow) {
					// If this regression fails, drain tracking before cleanup closes the DB.
					if infoCalls > 0 {
						waitForTracking()
					}
					require.Zero(t, infoCalls, "known blocked evidence must reject before provider requests")
					require.Zero(t, linkCalls)
					require.Contains(t, response.Header().Get("Location"), "/no_matching_file.mp4")
					return
				}
				require.Equal(t, 2, infoCalls, "the handler must validate files obtained for playback")
				if tc.rejected && (!allow || tc.rejectMixed) {
					require.Zero(t, linkCalls, "a blocked list must never reach link generation")
					require.Contains(t, response.Header().Get("Location"), "/no_matching_file.mp4")
				} else {
					require.Equal(t, 1, linkCalls, "video/subtitle lists must reach link generation")
					require.Contains(t, response.Header().Get("Location"), "/500.mp4")
				}
				// Tracking completes its database writes before sending this request.
				waitForTracking()
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h = ?", hash).Scan(&count))
				wantCount := len(tc.paths)
				if tc.name == "retained blocked evidence" {
					wantCount++
				}
				require.Equal(t, wantCount, count, "playback rejection must retain supplied file evidence")
			})
		}
	}
}
