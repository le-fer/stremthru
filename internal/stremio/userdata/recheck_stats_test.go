package stremio_userdata

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/debrider"
	"github.com/MunifTanjim/stremthru/store/easydebrid"
	"github.com/MunifTanjim/stremthru/store/premiumize"
	"github.com/MunifTanjim/stremthru/store/stats"
	"github.com/MunifTanjim/stremthru/store/torbox"
	"github.com/stretchr/testify/require"
)

func checkStatsSnapshot(name store.StoreName) stats.MethodStatsSnapshot {
	for _, entry := range stats.GetSnapshot(time.Hour) {
		if entry.Name == name {
			for _, method := range entry.Methods {
				if method.Name == "check_torz" {
					return method.MethodStatsSnapshot
				}
			}
		}
	}
	return stats.MethodStatsSnapshot{}
}

func TestRecheckRetriesAndCountsActualCalls(t *testing.T) {
	for _, name := range []store.StoreName{store.StoreNameTorBox, store.StoreNameDebrider, store.StoreNameEasyDebrid, store.StoreNamePremiumize} {
		for _, scenario := range []string{"retry", "mixed", "ordinary", "local only", "disabled", "batched", "cross-store reason"} {
			t.Run(string(name)+"/"+scenario, func(t *testing.T) {
				testutil.SetupDB(t)
				oldBuddy, oldPeer, oldRecheck := config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists
				config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = false, false, scenario != "disabled"
				t.Cleanup(func() {
					config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = oldBuddy, oldPeer, oldRecheck
				})
				count := 1
				if scenario == "batched" {
					count = 101
				}
				hashes := make([]string, count)
				for i := range hashes {
					hashes[i] = testutil.Hash(t)
					if scenario != "ordinary" && scenario != "cross-store reason" {
						_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(name.Code()), hashes[i])
						require.NoError(t, err)
					}
				}
				if scenario == "mixed" {
					hashes = append(hashes, testutil.Hash(t))
				}
				var calls atomic.Int64
				failure := errors.New("fixture API failure")
				httpClient := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, failure
				})}
				var provider store.Store
				switch name {
				case store.StoreNameTorBox:
					provider = torbox.NewStoreClient(&torbox.StoreClientConfig{HTTPClient: httpClient})
				case store.StoreNameDebrider:
					provider = debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: httpClient})
				case store.StoreNameEasyDebrid:
					provider = easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: httpClient})
				case store.StoreNamePremiumize:
					provider = premiumize.NewStoreClient(&premiumize.StoreClientConfig{HTTPClient: httpClient})
				}
				params := &store.CheckMagnetParams{Magnets: hashes, LocalOnly: scenario == "local only"}
				params.APIKey = "fixture"
				if scenario == "cross-store reason" {
					params.FileListRecheckHashes = map[string]struct{}{hashes[0]: {}}
				}
				before := checkStatsSnapshot(name)
				for range 2 {
					result, err := provider.CheckMagnet(params)
					if scenario == "ordinary" || scenario == "cross-store reason" {
						require.ErrorIs(t, err, failure)
						continue
					}
					require.NoError(t, err)
					require.Equal(t, store.MagnetStatusCached, result.Items[0].Status)
					if scenario != "local only" && scenario != "disabled" {
						require.ErrorIs(t, result.RecheckError, failure)
					}
				}
				after := checkStatsSnapshot(name)
				wantCalls := 2
				if scenario == "batched" && name == store.StoreNamePremiumize {
					wantCalls = 4
				}
				if scenario == "local only" || scenario == "disabled" {
					wantCalls = 0
				}
				actualCalls := int(calls.Load())
				require.Equal(t, wantCalls, actualCalls, "a failed recheck must retry on the next eligible request")
				require.EqualValues(t, actualCalls, after.TotalCount-before.TotalCount, "one event per API call")
				require.EqualValues(t, actualCalls, after.ErrorCount-before.ErrorCount)
				wantOnly, wantMixed, wantHashes := actualCalls, 0, 2*count
				if scenario == "mixed" {
					wantOnly, wantMixed = 0, actualCalls
				}
				if scenario == "ordinary" || wantCalls == 0 {
					wantOnly, wantMixed, wantHashes = 0, 0, 0
				}
				require.EqualValues(t, wantOnly, after.Rechecks.OnlyCalls-before.Rechecks.OnlyCalls)
				require.EqualValues(t, wantMixed, after.Rechecks.MixedCalls-before.Rechecks.MixedCalls)
				require.EqualValues(t, wantOnly+wantMixed, after.Rechecks.Errors-before.Rechecks.Errors)
				require.EqualValues(t, wantHashes, after.Rechecks.Hashes-before.Rechecks.Hashes)
			})
		}
	}
}

func TestCrossStoreStatsDistinguishRechecksFromOrdinaryFallback(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary only", true: "mixed"}[enabled], func(t *testing.T) {
			testutil.SetupDB(t)
			oldBuddy, oldPeer, oldRecheck, oldCross := config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore
			config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = false, false, true, enabled
			t.Cleanup(func() {
				config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists, config.StoreRecheckMissingFileListsCrossStore = oldBuddy, oldPeer, oldRecheck, oldCross
			})
			fileless, missing := testutil.Hash(t), testutil.Hash(t)
			first := &checkMagnetTestStore{name: store.StoreNameRealDebrid, items: []store.CheckMagnetDataItem{
				{Hash: fileless, Status: store.MagnetStatusCached},
				{Hash: missing, Status: store.MagnetStatusUnknown},
			}}
			client := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body easydebrid.LookupLinkDetailsParams
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				want := 1
				if enabled {
					want = 2
				}
				require.Len(t, body.URLs, want)
				return nil, errors.New("fixture fallback failure")
			})}
			second := easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: client})
			ud := UserDataStores{stores: []resolvedStore{{Store: first}, {Store: second, AuthToken: "fixture"}}}
			before := checkStatsSnapshot(store.StoreNameEasyDebrid)
			ud.CheckMagnet(&store.CheckMagnetParams{Magnets: []string{fileless, missing}}, logger.Scoped("test"))
			after := checkStatsSnapshot(store.StoreNameEasyDebrid)
			require.EqualValues(t, 1, after.TotalCount-before.TotalCount)
			want := int64(0)
			if enabled {
				want = 1
			}
			require.Equal(t, want, after.Rechecks.MixedCalls-before.Rechecks.MixedCalls)
			require.Equal(t, want, after.Rechecks.Hashes-before.Rechecks.Hashes)
		})
	}
}

func TestMalformedRecheckResponsesCountOneFailedCall(t *testing.T) {
	for _, name := range []store.StoreName{store.StoreNameDebrider, store.StoreNameEasyDebrid, store.StoreNamePremiumize} {
		t.Run(string(name), func(t *testing.T) {
			testutil.SetupDB(t)
			oldBuddy, oldPeer, oldRecheck := config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists
			config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = false, false, true
			t.Cleanup(func() {
				config.HasBuddy, config.HasPeer, config.StoreRecheckMissingFileLists = oldBuddy, oldPeer, oldRecheck
			})
			hash, missing := testutil.Hash(t), testutil.Hash(t)
			_, err := db.Exec("INSERT INTO magnet_cache (store,hash,is_cached,modified_at) VALUES (?,?,1,unixepoch())", string(name.Code()), hash)
			require.NoError(t, err)
			client := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				payload := map[string]any{"result": []map[string]any{{"cached": true}}}
				if name == store.StoreNamePremiumize {
					payload = map[string]any{"status": "success", "response": []bool{true}, "filename": []string{"file"}, "filesize": []int{1}}
				}
				return testutil.JSONResponse(r, 200, payload), nil
			})}
			var provider store.Store
			switch name {
			case store.StoreNameDebrider:
				provider = debrider.NewStoreClient(&debrider.StoreClientConfig{HTTPClient: client})
			case store.StoreNameEasyDebrid:
				provider = easydebrid.NewStoreClient(&easydebrid.StoreClientConfig{HTTPClient: client})
			case store.StoreNamePremiumize:
				provider = premiumize.NewStoreClient(&premiumize.StoreClientConfig{HTTPClient: client})
			}
			params := &store.CheckMagnetParams{Magnets: []string{hash, missing}}
			params.APIKey = "fixture"
			before := checkStatsSnapshot(name)
			result, err := provider.CheckMagnet(params)
			require.NoError(t, err)
			require.Error(t, result.RecheckError)
			after := checkStatsSnapshot(name)
			require.EqualValues(t, 1, after.TotalCount-before.TotalCount)
			require.EqualValues(t, 1, after.ErrorCount-before.ErrorCount)
			require.EqualValues(t, 1, after.Rechecks.MixedCalls-before.Rechecks.MixedCalls)
			require.EqualValues(t, 1, after.Rechecks.Errors-before.Rechecks.Errors)
		})
	}
}
