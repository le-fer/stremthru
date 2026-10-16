package stremio_shared

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/alldebrid"
	"github.com/MunifTanjim/stremthru/store/debrider"
	"github.com/MunifTanjim/stremthru/store/debridlink"
	"github.com/MunifTanjim/stremthru/store/easydebrid"
	"github.com/MunifTanjim/stremthru/store/offcloud"
	"github.com/MunifTanjim/stremthru/store/pikpak"
	"github.com/MunifTanjim/stremthru/store/premiumize"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/MunifTanjim/stremthru/store/torbox"
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/stretchr/testify/require"
)

func newFileValidationStore(name store.StoreName, client *http.Client) store.Store {
	switch name {
	case store.StoreNameAlldebrid:
		return alldebrid.NewStoreClient(&alldebrid.StoreClientConfig{HTTPClient: client})
	case store.StoreNameDebrider:
		return debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: client})
	case store.StoreNameDebridLink:
		return debridlink.NewStoreClient(&debridlink.StoreClientConfig{HTTPClient: client})
	case store.StoreNameEasyDebrid:
		return easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: client})
	case store.StoreNameOffcloud:
		return offcloud.NewStoreClient(&offcloud.StoreClientConfig{HTTPClient: client})
	case store.StoreNamePikPak:
		return pikpak.NewStoreClient(&pikpak.StoreClientConfig{HTTPClient: client})
	case store.StoreNamePremiumize:
		return premiumize.NewStoreClient(&premiumize.StoreClientConfig{HTTPClient: client})
	case store.StoreNameRealDebrid:
		return realdebrid.NewStoreClient(&realdebrid.StoreClientConfig{HTTPClient: client})
	case store.StoreNameTorBox:
		return torbox.NewStoreClient(&torbox.StoreClientConfig{HTTPClient: client})
	case store.StoreNameTorrin:
		return torrin.NewStoreClient(&torrin.StoreClientConfig{HTTPClient: client})
	default:
		panic("unsupported test provider: " + name)
	}
}

// These fixtures use each adapter's actual response types or JSON field names.
// They test normalization of returned evidence, not completeness of live APIs.
func providerPlaybackFixture(name store.StoreName, hash string, names []string) (string, map[string]any) {
	id := "1"
	responses := map[string]any{}
	files := []map[string]any{}
	for i, filename := range names {
		link := "https://fixture.invalid/" + filename
		switch name {
		case store.StoreNameAlldebrid:
			files = append(files, map[string]any{"n": filename, "s": 100, "l": link})
		case store.StoreNameDebridLink:
			files = append(files, map[string]any{"name": filename, "size": 100, "downloadUrl": link})
		case store.StoreNameDebrider:
			files = append(files, map[string]any{"name": "Movie/" + filename, "size": 100})
		case store.StoreNameEasyDebrid:
			files = append(files, map[string]any{"name": filename, "folder": "Movie", "size": 100})
		case store.StoreNameOffcloud, store.StoreNamePremiumize:
			files = append(files, map[string]any{"path": "Movie/" + filename, "size": 100, "url": link, "link": link})
		case store.StoreNamePikPak:
			files = append(files, map[string]any{"id": filename, "name": filename, "size": "100", "kind": "drive#file", "phase": "PHASE_TYPE_COMPLETE"})
		case store.StoreNameRealDebrid:
			files = append(files, map[string]any{"id": i + 1, "path": "/" + filename, "bytes": 100, "selected": 1})
		case store.StoreNameTorBox:
			files = append(files, map[string]any{"id": i, "name": "Movie/" + filename, "short_name": filename, "size": 100})
		case store.StoreNameTorrin:
			files = append(files, map[string]any{"index": i, "name": filename, "path": "/" + filename, "size": 100, "link": link})
		}
	}
	switch name {
	case store.StoreNameAlldebrid:
		responses["/v4.1/magnet/status"] = map[string]any{"status": "success", "data": map[string]any{"magnets": map[string]any{"id": 1, "hash": hash, "filename": "Movie", "statusCode": 4, "files": files}}}
	case store.StoreNameDebridLink:
		responses["/api/v2/seedbox/list"] = map[string]any{"success": true, "value": []map[string]any{{"id": id, "hashString": hash, "name": "Movie", "downloadPercent": 100, "files": files}}}
	case store.StoreNameDebrider:
		responses["/api/v1/tasks/1"] = map[string]any{"id": id, "hash": hash, "name": "Movie", "type": "torrent", "status": "completed", "files": files}
	case store.StoreNameEasyDebrid:
		id = "st:ed:" + hash
		responses["/api/v1/link/lookupdetails"] = map[string]any{"result": []map[string]any{{"cached": true, "files": files}}}
	case store.StoreNameOffcloud:
		responses["/api/cloud/explore/1"] = map[string]any{"files": files}
		responses["/api/cloud/history"] = []map[string]any{{"requestId": id, "fileName": "Movie", "status": "downloaded", "originalLink": "magnet:?xt=urn:btih:" + hash}}
	case store.StoreNamePikPak:
		responses["/v1/shield/captcha/init"] = map[string]any{"captcha_token": "fixture", "expires_in": 86400}
		responses["/v1/auth/signin"] = map[string]any{"access_token": "fixture", "recheck_token": "fixture", "sub": "fixture", "expires_in": 86400}
		responses["/drive/v1/files/1"] = map[string]any{"id": id, "name": "Movie", "phase": "PHASE_TYPE_COMPLETE", "kind": "drive#folder", "params": map[string]any{"url": "magnet:?xt=urn:btih:" + hash}}
		responses["/drive/v1/files"] = map[string]any{"files": files}
	case store.StoreNamePremiumize:
		id = premiumize.CachedMagnetIdPrefix + hash
		responses["/api/transfer/directdl"] = map[string]any{"status": "success", "content": files}
	case store.StoreNameRealDebrid:
		links := make([]string, len(names))
		for i, filename := range names {
			links[i] = "https://fixture.invalid/" + filename
		}
		responses["/rest/1.0/torrents/info/1"] = map[string]any{"id": id, "hash": hash, "filename": "Movie", "status": "downloaded", "files": files, "links": links}
	case store.StoreNameTorBox:
		responses["/v1/api/torrents/mylist"] = map[string]any{"success": true, "data": map[string]any{"id": 1, "hash": hash, "name": "Movie", "download_finished": true, "download_present": true, "files": files}}
	case store.StoreNameTorrin:
		responses["/v0/store/magnets/1"] = map[string]any{"data": map[string]any{"id": id, "hash": hash, "name": "Movie", "status": "downloaded", "files": files}}
	}
	return id, responses
}

func TestProviderPlaybackFileValidation(t *testing.T) {
	for _, name := range store.StoreNames {
		for _, names := range [][]string{{"movie.mkv"}, {"movie.mkv", "setup.exe"}, {"setup.exe"}, {"readme.txt"}, {}} {
			t.Run(fmt.Sprintf("%s/%v", name, names), func(t *testing.T) {
				testutil.SetupDB(t)
				hash := testutil.Hash(t)
				id, responses := providerPlaybackFixture(name, hash, names)
				client := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					body, ok := responses[r.URL.Path]
					if !ok {
						t.Errorf("unexpected %s request: %s", name, r.URL.Path)
						return nil, errors.New("unexpected fixture request")
					}
					return testutil.JSONResponse(r, 200, body), nil
				})}
				provider := newFileValidationStore(name, client)
				params := &store.GetMagnetParams{Id: id}
				params.APIKey = "fixture:" + hash
				data, err := provider.GetMagnet(params)
				require.NoError(t, err)
				require.Equal(t, store.MagnetStatusDownloaded, data.Status)
				require.Len(t, data.Files, len(names), "adapter must retain disallowed and non-video files")
				for i, filename := range names {
					require.Equal(t, filename, data.Files[i].Name)
					if name == store.StoreNameTorrin {
						require.Equal(t, i, data.Files[i].Idx, "remote file indices must survive decoding")
					}
				}
				original := slices.Clone(data.Files)
				for _, allow := range []bool{false, true} {
					videos, err := ValidatePlaybackFiles(data.Files, PlaybackFileRequest{
						Index:      -1,
						StoreCode:  name.Code(),
						AllowMixed: allow,
					})
					wantVideo := slices.Contains(names, "movie.mkv") && (allow || !slices.Contains(names, "setup.exe"))
					if wantVideo {
						require.NoError(t, err)
						require.Len(t, videos, 1)
						require.Equal(t, "movie.mkv", videos[0].GetName())
					} else {
						require.Error(t, err)
						require.Empty(t, videos)
					}
					if slices.Contains(names, "setup.exe") {
						_, err := ValidatePlaybackFiles(data.Files, PlaybackFileRequest{
							Name:       "setup.exe",
							Index:      -1,
							StoreCode:  name.Code(),
							AllowMixed: allow,
						})
						require.EqualError(t, err, "requested file has a disallowed extension")
					}
					require.Equal(t, original, data.Files)
				}
			})
		}
	}
}

func TestCacheBackedProvidersRetainFileEvidence(t *testing.T) {
	previousBuddy, previousPeer, previousRecheck := config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists
	config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = false, false, false
	t.Cleanup(func() {
		config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = previousBuddy, previousPeer, previousRecheck
	})
	for _, name := range store.StoreNames {
		if name == store.StoreNameOffcloud || name == store.StoreNameTorrin {
			continue // These adapters use direct or remote availability responses.
		}
		t.Run(string(name), func(t *testing.T) {
			testutil.SetupDB(t)
			hash := testutil.Hash(t)
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{
				{Hash: hash, File: torrent_stream.File{Path: "/movie.mkv"}},
				{Hash: hash, File: torrent_stream.File{Path: "/setup.exe"}},
			}, false))
			_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(name.Code()), hash)
			require.NoError(t, err)
			provider := newFileValidationStore(name, &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				t.Errorf("cached evidence unexpectedly triggered a provider request: %s", r.URL.Path)
				return nil, errors.New("unexpected provider request")
			})})
			params := &store.CheckMagnetParams{Magnets: []string{hash}, IsTrustedRequest: true}
			params.APIKey = "fixture-token"
			data, err := provider.CheckMagnet(params)
			require.NoError(t, err)
			require.Len(t, data.Items, 1)
			require.Equal(t, store.MagnetStatusCached, data.Items[0].Status)
			require.Len(t, data.Items[0].Files, 2)
			require.True(t, slices.ContainsFunc(data.Items[0].Files, func(f store.MagnetFile) bool { return filepath.Ext(f.Name) == ".exe" }))
		})
	}
}
