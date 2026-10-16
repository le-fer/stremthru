package stremio_userdata

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/debrider"
	"github.com/MunifTanjim/stremthru/store/easydebrid"
	"github.com/stretchr/testify/require"
)

func TestCheckMagnetMixedTorrentPreference(t *testing.T) {
	previousAllow, previousExtensions := config.AllowVideoTorrentsWithDisallowedFiles, util.FileExtDisallowed
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles, util.FileExtDisallowed = previousAllow, previousExtensions
	})
	var err error
	util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
	if err != nil {
		t.Fatal(err)
	}
	const hash = "0123456789012345678901234567890123456789"
	for _, allow := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			files  []store.MagnetFile
			viable bool
		}{
			{"mixed", []store.MagnetFile{{Name: "movie.mp4"}, {Name: "setup.exe"}}, true},
			{"blocked video", []store.MagnetFile{{Name: "movie.mkv"}}, false},
			{"blocked and allowed videos", []store.MagnetFile{{Name: "movie.mkv"}, {Path: "/movie.mp4"}}, true},
			{"no video", []store.MagnetFile{{Name: "setup.exe"}}, false},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.name, allow), func(t *testing.T) {
				config.AllowVideoTorrentsWithDisallowedFiles = allow
				first := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: tc.files}}
				ud := UserDataStores{stores: []resolvedStore{{Store: first}}}
				res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
				_, valid := res.ByHash[hash]
				if valid != (allow && tc.viable) {
					t.Fatalf("valid=%v allow=%v viable=%v", valid, allow, tc.viable)
				}
				if !reflect.DeepEqual(first.item.Files, tc.files) {
					t.Fatal("validation changed raw file evidence")
				}
				// Strict rejection survives fallback. In mixed mode, an allowed video
				// from the fallback can clear a rejection for lacking an allowed video.
				fallback := &checkMagnetTestStore{name: store.StoreNameTorBox, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mp4"}}}}
				ud.stores = append(ud.stores, resolvedStore{Store: fallback})
				res = ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
				_, valid = res.ByHash[hash]
				if valid != allow {
					t.Fatalf("fallback valid=%v allow=%v", valid, allow)
				}
			})
		}
	}
}

func TestBuddyFileEvidenceIndependentOfInstancePreference(t *testing.T) {
	testutil.SetupDB(t)
	hash := testutil.Hash(t)
	if err := torrent_stream.Record([]torrent_stream.InsertData{
		{Hash: hash, File: torrent_stream.File{Name: "movie.mkv", Path: "/movie.mkv"}},
		{Hash: hash, File: torrent_stream.File{Name: "setup.exe", Path: "/setup.exe"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('rd',?,1,unixepoch())", hash); err != nil {
		t.Fatal(err)
	}
	previousAllow := config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previousAllow })
	s := &checkMagnetTestStore{name: store.StoreNameRealDebrid}
	for _, allow := range []bool{false, true} {
		config.AllowVideoTorrentsWithDisallowedFiles = allow
		data, err := buddy.CheckMagnet(s, []string{hash}, "fixture", "", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(data.Items) != 1 || len(data.Items[0].Files) != 2 || !slices.ContainsFunc(data.Items[0].Files, func(file store.MagnetFile) bool { return file.Name == "setup.exe" }) {
			t.Fatalf("lost shared evidence: %+v", data)
		}
		if viable := validateMagnetFiles(data.Items[0], allow).viable; viable != allow {
			t.Fatalf("viable=%v allow=%v", viable, allow)
		}
	}
}

func TestEasyDebridOmittedMissFallsBack(t *testing.T) {
	previousRecheck, previousCross, previousBuddy, previousPeer := config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore, config.HasBuddy, config.HasPeer
	t.Cleanup(func() {
		config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore, config.HasBuddy, config.HasPeer = previousRecheck, previousCross, previousBuddy, previousPeer
	})
	config.HasBuddy, config.HasPeer = false, false
	for _, flags := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		for _, cached := range []bool{false, true} {
			if cached && !flags[0] {
				continue // Disabled recheck intentionally retains a cached fileless answer.
			}
			t.Run(fmt.Sprintf("recheck=%t/cross=%t/cached=%t", flags[0], flags[1], cached), func(t *testing.T) {
				testutil.SetupDB(t)
				config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = flags[0], flags[1]
				hash := testutil.Hash(t)
				if cached {
					_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES ('ed',?,1,unixepoch())", hash)
					require.NoError(t, err)
				}
				first := easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					require.Contains(t, r.URL.Path, "/lookupdetails")
					return testutil.JSONResponse(r, 200, map[string]any{"result": []map[string]any{{"cached": false}}}), nil
				})}})
				second := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}}}
				ud := UserDataStores{stores: []resolvedStore{{Store: first, AuthToken: "fixture-token"}, {Store: second}}}
				// Normalize a magnet URL and deduplicate repeated omitted requests.
				result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{"magnet:?xt=urn:btih:" + hash, hash}}, logger.Scoped("test"))
				require.False(t, result.HasErr)
				require.Equal(t, 1, second.calls)
				require.Equal(t, "RD", result.ByHash[hash])
			})
		}
	}
}

func TestAmbiguousProviderBatchFallsBack(t *testing.T) {
	for _, provider := range []store.StoreName{store.StoreNameEasyDebrid, store.StoreNameDebrider} {
		for _, recheck := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recheck=%t", provider, recheck), func(t *testing.T) {
				testutil.SetupDB(t)
				oldBuddy, oldPeer := config.HasBuddy, config.HasPeer
				oldRecheck, oldCross := config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore
				config.HasBuddy, config.HasPeer = false, false
				config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = true, false
				t.Cleanup(func() {
					config.HasBuddy, config.HasPeer = oldBuddy, oldPeer
					config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = oldRecheck, oldCross
				})
				first, second := testutil.Hash(t), testutil.Hash(t)
				if recheck {
					_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(provider.Code()), first)
					require.NoError(t, err)
				}
				calls := 0
				httpClient := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					return testutil.JSONResponse(r, 200, map[string]any{"result": []map[string]any{{"cached": true, "files": []map[string]any{{"name": "ambiguous.mkv"}}}}}), nil
				})}
				var adapter store.Store
				if provider == store.StoreNameEasyDebrid {
					adapter = easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: httpClient})
				} else {
					adapter = debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: httpClient})
				}
				fallback := &checkMagnetTestStore{name: store.StoreNameRealDebrid, items: []store.CheckMagnetDataItem{
					{Hash: first, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "first.mkv"}}},
					{Hash: second, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "second.mkv"}}},
				}}
				ud := UserDataStores{stores: []resolvedStore{{Store: adapter, AuthToken: "fixture-token"}, {Store: fallback}}}
				result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{first, second}}, logger.Scoped("test"))
				require.True(t, result.HasErr, "the ambiguous provider response must remain observable")
				require.Equal(t, 1, calls)
				require.Equal(t, 1, fallback.calls, "ordinary unresolved hashes still fall back with cross-store rechecking disabled")
				require.Equal(t, "first.mkv", result.FilesByHash[first][0].Name)
				require.Equal(t, "second.mkv", result.FilesByHash[second][0].Name)
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_stream WHERE h IN (?,?)", first, second).Scan(&count))
				require.Zero(t, count, "invalid provider batches must not be recorded")
			})
		}
	}
}

type checkMagnetTestStore struct {
	store.Store
	recheckErr error
	name       store.StoreName
	item       store.CheckMagnetDataItem
	items      []store.CheckMagnetDataItem
	err        error
	calls      int
	check      func()
	localOnly  []bool
}

func (s *checkMagnetTestStore) GetName() store.StoreName { return s.name }

func (s *checkMagnetTestStore) CheckMagnet(params *store.CheckMagnetParams) (*store.CheckMagnetData, error) {
	s.calls++
	s.localOnly = append(s.localOnly, params.LocalOnly)
	if s.check != nil {
		s.check()
	}
	if s.err != nil {
		return nil, s.err
	}
	items := s.items
	if items == nil {
		items = []store.CheckMagnetDataItem{s.item}
	}
	return &store.CheckMagnetData{Items: items, RecheckError: s.recheckErr}, nil
}

func TestCheckMagnetForwardsLocalOnlyToEveryStore(t *testing.T) {
	const hash = "0123456789012345678901234567890123456789"
	first := &checkMagnetTestStore{name: store.StoreNameAlldebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown}}
	second := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached}}
	ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: second}}}

	ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}, LocalOnly: true}, logger.Scoped("test"))
	require.Equal(t, []bool{true}, first.localOnly)
	require.Equal(t, []bool{true}, second.localOnly)
}

func TestCheckMagnetConcurrentFallbackErrors(t *testing.T) {
	const hash = "0123456789012345678901234567890123456789"
	first := &checkMagnetTestStore{name: store.StoreNameAlldebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown}}
	var ready sync.WaitGroup
	ready.Add(2)
	check := func() {
		ready.Done()
		ready.Wait()
	}
	errRD := errors.New("RD failed")
	errTB := errors.New("TB failed")
	ud := UserDataStores{stores: []resolvedStore{
		{Store: first},
		{Store: &checkMagnetTestStore{name: store.StoreNameRealDebrid, err: errRD, check: check}},
		{Store: &checkMagnetTestStore{name: store.StoreNameTorBox, err: errTB, check: check}},
	}}
	res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
	if !res.HasErr || res.Err[0] != nil || res.Err[1] != errRD || res.Err[2] != errTB {
		t.Fatalf("unexpected errors: HasErr=%v Err=%v", res.HasErr, res.Err)
	}
	if !reflect.DeepEqual(res.HasErrByStoreCode, map[string]struct{}{"RD": {}, "TB": {}}) {
		t.Fatalf("unexpected failing stores: %v", res.HasErrByStoreCode)
	}
	if len(res.ByHash) != 0 || len(res.InvalidByHash) != 0 {
		t.Fatalf("unexpected cached or invalid results: %v %v", res.ByHash, res.InvalidByHash)
	}
}

func TestCheckMagnetNilFallbackStores(t *testing.T) {
	const hash = "0123456789012345678901234567890123456789"
	ud := UserDataStores{stores: []resolvedStore{
		{Store: &checkMagnetTestStore{name: store.StoreNameAlldebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown}}},
		{},
		{},
	}}
	res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
	if !res.HasErr || res.Err[0] != nil || res.Err[1] == nil || res.Err[2] == nil {
		t.Fatalf("unexpected errors: HasErr=%v Err=%v", res.HasErr, res.Err)
	}
	for _, err := range res.Err[1:] {
		if err.Error() != "invalid userdata, invalid store" {
			t.Fatalf("unexpected fallback error: %v", err)
		}
	}
	if len(res.HasErrByStoreCode) != 0 || len(res.ByHash) != 0 || len(res.InvalidByHash) != 0 {
		t.Fatalf("unexpected store results: %+v", res)
	}
}

func TestCheckMagnetNilPrimaryStore(t *testing.T) {
	ud := UserDataStores{stores: []resolvedStore{{}}}
	res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{strings.Repeat("a", 40)}}, logger.Scoped("test"))
	if !res.HasErr || len(res.Err) != 1 || res.Err[0] == nil || res.Err[0].Error() != "invalid userdata, invalid store" {
		t.Fatalf("unexpected primary-store result: %+v", res)
	}
}

func TestCheckMagnetFileValidation(t *testing.T) {
	originalRecheck := config.StoreRecheckMissingFileLists
	originalCrossStore := config.StoreRecheckMissingFileListsCrossStore
	config.StoreRecheckMissingFileLists = true
	config.StoreRecheckMissingFileListsCrossStore = true
	t.Cleanup(func() {
		config.StoreRecheckMissingFileLists = originalRecheck
		config.StoreRecheckMissingFileListsCrossStore = originalCrossStore
	})
	const hash = "0123456789012345678901234567890123456789"
	item := func(status store.MagnetStatus, files ...store.MagnetFile) store.CheckMagnetDataItem {
		return store.CheckMagnetDataItem{Hash: hash, Status: status, Files: files}
	}
	video := item(store.MagnetStatusCached, store.MagnetFile{Name: "movie.MKV"})
	pathVideo := item(store.MagnetStatusCached, store.MagnetFile{Path: "/movie.mp4"})
	nonVideo := item(store.MagnetStatusCached, store.MagnetFile{Name: "movie.mkv.exe"})
	noVideo := item(store.MagnetStatusCached, store.MagnetFile{Name: "readme.txt"})
	fileless := item(store.MagnetStatusCached)
	unknown := item(store.MagnetStatusUnknown)
	mixed := item(store.MagnetStatusCached, store.MagnetFile{Name: "movie.mkv"}, store.MagnetFile{Name: "setup.exe"})
	provider := func(name store.StoreName, result store.CheckMagnetDataItem) checkMagnetTestStore {
		return checkMagnetTestStore{name: name, item: result}
	}
	tests := []struct {
		name    string
		stores  []checkMagnetTestStore
		cached  string
		invalid bool
		calls   []int
	}{
		{"torbox empty", []checkMagnetTestStore{provider(store.StoreNameTorBox, fileless)}, "TB", false, []int{1}},
		{"fileless provider", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, fileless)}, "RD", false, []int{1}},
		{"executable only", []checkMagnetTestStore{provider(store.StoreNameTorBox, nonVideo)}, "", true, []int{1}},
		{"video name", []checkMagnetTestStore{provider(store.StoreNameTorBox, video)}, "TB", false, []int{1}},
		{"video path", []checkMagnetTestStore{provider(store.StoreNameTorBox, pathVideo)}, "TB", false, []int{1}},
		{"mixed files", []checkMagnetTestStore{provider(store.StoreNameTorBox, mixed)}, "", true, []int{1}},
		{"blocked mixed list skips fileless fallback", []checkMagnetTestStore{provider(store.StoreNameTorBox, mixed), provider(store.StoreNameRealDebrid, fileless)}, "", true, []int{1, 0}},
		{"blocked mixed list rejects provisional hit", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, fileless), provider(store.StoreNameTorBox, mixed)}, "", true, []int{1, 1}},
		{"unknown is not rejected", []checkMagnetTestStore{provider(store.StoreNameTorBox, unknown)}, "", false, []int{1}},
		{"disallowed rejection skips fileless fallback", []checkMagnetTestStore{provider(store.StoreNameTorBox, nonVideo), provider(store.StoreNameRealDebrid, fileless)}, "", true, []int{1, 0}},
		{"fileless cannot hide rejection", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, fileless), provider(store.StoreNameTorBox, nonVideo)}, "", true, []int{1, 1}},
		{"fileless providers remain compatible", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, fileless), provider(store.StoreNameTorBox, fileless)}, "RD", false, []int{1, 1}},
		{"disallowed rejection skips video fallback", []checkMagnetTestStore{provider(store.StoreNameTorBox, nonVideo), provider(store.StoreNameRealDebrid, video)}, "", true, []int{1, 0}},
		{"verified first store does not query another store", []checkMagnetTestStore{provider(store.StoreNameTorBox, video), provider(store.StoreNameRealDebrid, nonVideo)}, "TB", false, []int{1, 0}},
		{"mixed disallowed rejection skips video fallback", []checkMagnetTestStore{provider(store.StoreNameTorBox, mixed), provider(store.StoreNameRealDebrid, video)}, "", true, []int{1, 0}},
		{"no video only", []checkMagnetTestStore{provider(store.StoreNameTorBox, noVideo)}, "", true, []int{1}},
		{"video clears no-video rejection", []checkMagnetTestStore{provider(store.StoreNameTorBox, noVideo), provider(store.StoreNameRealDebrid, video)}, "RD", false, []int{1, 1}},
		{"verified video skips later no-video store", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, video), provider(store.StoreNameTorBox, noVideo)}, "RD", false, []int{1, 0}},
		{"fileless cannot clear no-video rejection", []checkMagnetTestStore{provider(store.StoreNameTorBox, noVideo), provider(store.StoreNameRealDebrid, fileless)}, "", true, []int{1, 1}},
		{"video replaces provisional hit", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, fileless), provider(store.StoreNameTorBox, video)}, "TB", false, []int{1, 1}},
		{"verified first store short circuits", []checkMagnetTestStore{provider(store.StoreNameTorBox, video), provider(store.StoreNameRealDebrid, unknown)}, "TB", false, []int{1, 0}},
		{"provisional hit survives unknown", []checkMagnetTestStore{provider(store.StoreNameRealDebrid, fileless), provider(store.StoreNameTorBox, unknown)}, "RD", false, []int{1, 1}},
		{"fallback fileless then rejection", []checkMagnetTestStore{provider(store.StoreNameAlldebrid, unknown), provider(store.StoreNameRealDebrid, fileless), provider(store.StoreNameTorBox, nonVideo)}, "", true, []int{1, 1, 1}},
		{"fallback rejection then fileless", []checkMagnetTestStore{provider(store.StoreNameAlldebrid, unknown), provider(store.StoreNameTorBox, nonVideo), provider(store.StoreNameRealDebrid, fileless)}, "", true, []int{1, 1, 1}},
		{"fallback video and disallowed files", []checkMagnetTestStore{provider(store.StoreNameAlldebrid, unknown), provider(store.StoreNameRealDebrid, video), provider(store.StoreNameTorBox, nonVideo)}, "", true, []int{1, 1, 1}},
		{"fallback disallowed files and video", []checkMagnetTestStore{provider(store.StoreNameAlldebrid, unknown), provider(store.StoreNameTorBox, nonVideo), provider(store.StoreNameRealDebrid, video)}, "", true, []int{1, 1, 1}},
		{"fallback video and no-video rejection", []checkMagnetTestStore{provider(store.StoreNameAlldebrid, unknown), provider(store.StoreNameRealDebrid, video), provider(store.StoreNameTorBox, noVideo)}, "RD", false, []int{1, 1, 1}},
		{"fallback no-video rejection and video", []checkMagnetTestStore{provider(store.StoreNameAlldebrid, unknown), provider(store.StoreNameTorBox, noVideo), provider(store.StoreNameRealDebrid, video)}, "RD", false, []int{1, 1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ud := UserDataStores{}
			for i := range tt.stores {
				ud.stores = append(ud.stores, resolvedStore{Store: &tt.stores[i]})
			}
			res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
			_, invalid := res.InvalidByHash[hash]
			if res.HasErr || res.ByHash[hash] != tt.cached || invalid != tt.invalid {
				t.Fatalf("cached=%q invalid=%v errors=%v; want cached=%q invalid=%v", res.ByHash[hash], invalid, res.Err, tt.cached, tt.invalid)
			}
			calls := make([]int, len(tt.stores))
			for i := range tt.stores {
				calls[i] = tt.stores[i].calls
			}
			if !reflect.DeepEqual(calls, tt.calls) {
				t.Fatalf("calls=%v; want %v", calls, tt.calls)
			}
		})
	}
}

func TestCheckMagnetFallbackAfterError(t *testing.T) {
	const hash = "0123456789012345678901234567890123456789"
	video := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}}
	first := &checkMagnetTestStore{name: store.StoreNameAlldebrid, err: errors.New("first store unavailable")}
	second := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: video}
	ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: second}}}
	res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
	if !res.HasErr || res.Err[0] != first.err || res.ByHash[hash] != "RD" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, ok := res.HasErrByStoreCode["AD"]; !ok {
		t.Fatal("missing failed store code")
	}
}

func TestCheckMagnetBlockedExtensions(t *testing.T) {
	const hash = "0123456789012345678901234567890123456789"
	for _, ext := range strings.Fields(".exe .com .scr .msi .bat .cmd .ps1 .vbs .js .jar .lnk .sh .bash .zsh .fish .run .appimage .deb .rpm .app .dmg .pkg .command .scpt .scptd .aab .apk .apks .apkm .xapk") {
		for _, field := range []string{"name", "path"} {
			for _, blockedFirst := range []bool{false, true} {
				t.Run(ext+"/"+field+"/"+fmt.Sprint(blockedFirst), func(t *testing.T) {
					blocked := store.MagnetFile{Name: "movie.mkv", Path: "/movie.mkv"}
					if field == "name" {
						blocked.Name = "setup" + strings.ToUpper(ext)
					} else {
						blocked.Path = "/setup" + strings.ToUpper(ext)
					}
					video := store.MagnetFile{Name: "movie.mkv", Path: "/movie.mkv"}
					files := []store.MagnetFile{video, blocked}
					if blockedFirst {
						files = []store.MagnetFile{blocked, video}
					}
					provider := &checkMagnetTestStore{name: store.StoreNameTorBox, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: files}}
					ud := UserDataStores{stores: []resolvedStore{{Store: provider}}}
					res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
					if _, invalid := res.InvalidByHash[hash]; !invalid || len(res.ByHash) != 0 || res.HasErr {
						t.Fatalf("blocked torrent was not rejected: %+v", res)
					}
				})
			}
		}
	}
}

func TestValidateMagnetFilesAllowedFiles(t *testing.T) {
	for _, files := range [][]store.MagnetFile{
		{{Name: "movie.mkv"}, {Name: "subtitle.srt"}, {Name: "readme.txt"}, {Name: "cover.jpg"}},
		{{Name: "movie.mkv", Path: "/folder"}},
		{{Name: "movie.mkv", Path: "/example.com/movie.mkv"}},
		{{Name: "movie.mkv", Path: `C:\example.COM\movie.mkv`}},
		nil,
	} {
		if !validateMagnetFiles(store.CheckMagnetDataItem{Status: store.MagnetStatusCached, Files: files}, false).viable {
			t.Fatalf("rejected compatible file list: %+v", files)
		}
	}
}

func TestCheckMagnetCrossStoreRecheckSetting(t *testing.T) {
	const hash = "0123456789012345678901234567890123456789"
	cachedWithoutFiles := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached}
	video := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}}
	noVideo := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "readme.txt"}}}
	disallowed := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "setup.exe"}}}
	unknown := store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusUnknown}
	originalRecheck := config.StoreRecheckMissingFileLists
	originalCrossStore := config.StoreRecheckMissingFileListsCrossStore
	t.Cleanup(func() {
		config.StoreRecheckMissingFileLists = originalRecheck
		config.StoreRecheckMissingFileListsCrossStore = originalCrossStore
	})
	for _, tc := range []struct {
		name          string
		recheck       bool
		crossStore    bool
		first         store.CheckMagnetDataItem
		firstError    bool
		cached        string
		invalid       bool
		fallbackCalls int
	}{
		{"both enabled recheck missing file list", true, true, cachedWithoutFiles, false, "TB", false, 1},
		{"cross-store disabled keeps cached result", true, false, cachedWithoutFiles, false, "RD", false, 0},
		{"master disabled overrides cross-store", false, true, cachedWithoutFiles, false, "RD", false, 0},
		{"both disabled keep cached result", false, false, cachedWithoutFiles, false, "RD", false, 0},
		{"valid file list skips fallback", true, true, video, false, "RD", false, 0},
		{"cache miss still falls back with master disabled", false, true, unknown, false, "TB", false, 1},
		{"cache miss still falls back with both disabled", false, false, unknown, false, "TB", false, 1},
		{"request error still falls back with both disabled", false, false, unknown, true, "TB", false, 1},
		{"no-video failure still falls back with both disabled", false, false, noVideo, false, "TB", false, 1},
		{"disallowed rejection skips fallback with both disabled", false, false, disallowed, false, "", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config.StoreRecheckMissingFileLists = tc.recheck
			config.StoreRecheckMissingFileListsCrossStore = tc.crossStore
			first := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: tc.first}
			if tc.firstError {
				first.err = errors.New("first store unavailable")
			}
			fallback := &checkMagnetTestStore{name: store.StoreNameTorBox, item: video}
			ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: fallback}}}
			res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
			_, invalid := res.InvalidByHash[hash]
			if res.ByHash[hash] != tc.cached || invalid != tc.invalid || fallback.calls != tc.fallbackCalls || res.HasErr != tc.firstError {
				t.Fatalf("cached=%q invalid=%v fallback calls=%d errors=%v; want cached=%q invalid=%v calls=%d error=%v", res.ByHash[hash], invalid, fallback.calls, res.Err, tc.cached, tc.invalid, tc.fallbackCalls, tc.firstError)
			}
		})
	}
}

func TestCheckMagnetRecheckWarningPreservesCachedAndFallbackPolicy(t *testing.T) {
	oldRecheck, oldCross := config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore
	t.Cleanup(func() {
		config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = oldRecheck, oldCross
	})
	const hash = "0123456789012345678901234567890123456789"
	for _, cross := range []bool{false, true} {
		config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = true, cross
		warning := errors.New("additional recheck unavailable")
		first := &checkMagnetTestStore{name: store.StoreNameTorBox, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached}, recheckErr: warning}
		second := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}}}
		ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: second}}}
		res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
		if !res.HasErr || !errors.Is(res.Err[0], warning) {
			t.Fatalf("recheck failure was not retained: %+v", res)
		}
		if _, failed := res.HasErrByStoreCode["TB"]; !failed {
			t.Fatalf("recheck failure did not mark the provider unavailable: %+v", res.HasErrByStoreCode)
		}
		expected, calls := "TB", 0
		if cross {
			expected, calls = "RD", 1
		}
		if res.ByHash[hash] != expected || second.calls != calls {
			t.Fatalf("cached=%s calls=%d", res.ByHash[hash], second.calls)
		}
	}
}

func TestCheckMagnetRecheckFailureDoesNotAdvertiseUnresolvedHash(t *testing.T) {
	oldRecheck := config.StoreRecheckMissingFileLists
	config.StoreRecheckMissingFileLists = true
	t.Cleanup(func() { config.StoreRecheckMissingFileLists = oldRecheck })

	const cachedHash = "0123456789012345678901234567890123456789"
	const unresolvedHash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	warning := errors.New("additional recheck unavailable")
	recheckedItems := []store.CheckMagnetDataItem{
		{Hash: cachedHash, Status: store.MagnetStatusCached},
		{Hash: unresolvedHash, Status: store.MagnetStatusUnknown},
	}
	unknownItems := []store.CheckMagnetDataItem{
		{Hash: cachedHash, Status: store.MagnetStatusUnknown},
		{Hash: unresolvedHash, Status: store.MagnetStatusUnknown},
	}
	for _, tc := range []struct {
		name     string
		stores   []resolvedStore
		errorIdx int
	}{
		{
			name: "primary store",
			stores: []resolvedStore{{Store: &checkMagnetTestStore{
				name: store.StoreNameTorBox, recheckErr: warning, items: recheckedItems,
			}}},
			errorIdx: 0,
		},
		{
			name: "fallback store",
			stores: []resolvedStore{
				{Store: &checkMagnetTestStore{name: store.StoreNameRealDebrid, items: unknownItems}},
				{Store: &checkMagnetTestStore{name: store.StoreNameTorBox, recheckErr: warning, items: recheckedItems}},
			},
			errorIdx: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ud := UserDataStores{stores: tc.stores}
			res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{cachedHash, unresolvedHash}}, logger.Scoped("test"))

			require.Equal(t, "TB", res.ByHash[cachedHash])
			require.NotContains(t, res.ByHash, unresolvedHash)
			require.True(t, res.HasErr)
			require.ErrorIs(t, res.Err[tc.errorIdx], warning)
			require.Contains(t, res.HasErrByStoreCode, "TB")
		})
	}
}

func TestReturnedFileValidationForEveryStore(t *testing.T) {
	previous := config.AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() { config.AllowVideoTorrentsWithDisallowedFiles = previous })
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	for _, name := range store.StoreNames {
		for _, allow := range []bool{false, true} {
			for _, tc := range []struct {
				name  string
				files []store.MagnetFile
				valid bool
			}{
				{"missing list", nil, true},
				{"video", []store.MagnetFile{{Name: "movie.mkv"}}, true},
				{"bundle descendant video", []store.MagnetFile{{Name: "movie.mkv", Path: "/Example.app/Contents/movie.mkv"}}, true},
				{"nonvideo", []store.MagnetFile{{Name: "readme.txt"}}, false},
				{"executable", []store.MagnetFile{{Name: "setup.exe"}}, false},
				{"mixed", []store.MagnetFile{{Name: "movie.mkv"}, {Name: "setup.exe"}}, allow},
			} {
				t.Run(fmt.Sprintf("%s/%s/allow=%t", name, tc.name, allow), func(t *testing.T) {
					config.AllowVideoTorrentsWithDisallowedFiles = allow
					provider := &checkMagnetTestStore{name: name, item: store.CheckMagnetDataItem{Hash: strings.ToUpper(hash), Status: store.MagnetStatusCached, Files: tc.files}}
					ud := UserDataStores{stores: []resolvedStore{{Store: provider}}}
					result := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
					_, valid := result.ByHash[hash]
					_, invalid := result.InvalidByHash[hash]
					if result.HasErr || valid != tc.valid || invalid == tc.valid {
						t.Fatalf("unexpected validation: %+v", result)
					}
				})
			}
		}
	}
}

func TestCheckMagnetCanonicalResponseHashesAcrossStores(t *testing.T) {
	prevAllow, prevRecheck, prevCross := config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore
	config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = false, true, true
	t.Cleanup(func() {
		config.AllowVideoTorrentsWithDisallowedFiles, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = prevAllow, prevRecheck, prevCross
	})
	hash := strings.Repeat("ab", 20)
	for _, blockedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked-first=%t", blockedFirst), func(t *testing.T) {
			firstFiles := []store.MagnetFile{}
			secondFiles := []store.MagnetFile{{Name: "setup.exe"}}
			if blockedFirst {
				firstFiles, secondFiles = secondFiles, []store.MagnetFile{{Name: "Movie.MKV"}}
			}
			first := &checkMagnetTestStore{name: store.StoreNameTorBox, item: store.CheckMagnetDataItem{Hash: strings.ToUpper(hash), Status: store.MagnetStatusCached, Files: firstFiles}}
			second := &checkMagnetTestStore{name: store.StoreNameRealDebrid, item: store.CheckMagnetDataItem{Hash: hash, Status: store.MagnetStatusCached, Files: secondFiles}}
			ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: second}}}
			res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{strings.ToUpper(hash), hash}}, logger.Scoped("test"))
			require.False(t, res.HasErr)
			expectedCalls := 1
			if blockedFirst {
				expectedCalls = 0
			}
			require.Equal(t, expectedCalls, second.calls)
			require.Empty(t, res.ByHash)
			require.Equal(t, map[string]struct{}{hash: {}}, res.InvalidByHash)
		})
	}
	first := &checkMagnetTestStore{name: store.StoreNameTorBox, item: store.CheckMagnetDataItem{Hash: strings.ToUpper(hash), Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "Movie.MKV"}}}}
	ud := UserDataStores{stores: []resolvedStore{{Store: first}}}
	res := ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{hash}}, logger.Scoped("test"))
	require.Equal(t, map[string]string{hash: "TB"}, res.ByHash)
	require.Empty(t, res.InvalidByHash)
}
