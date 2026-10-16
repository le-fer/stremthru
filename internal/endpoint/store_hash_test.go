package endpoint

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	storecontext "github.com/MunifTanjim/stremthru/internal/store/context"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

type cachedListHashTestStore struct {
	store.Store
	data *store.ListMagnetsData
	name store.StoreName
}

type checkMagnetInputTestStore struct {
	store.Store
	got   []string
	items []store.CheckMagnetDataItem
}

func (s *checkMagnetInputTestStore) GetName() store.StoreName { return store.StoreNameTorrin }
func (s *checkMagnetInputTestStore) CheckMagnet(params *store.CheckMagnetParams) (*store.CheckMagnetData, error) {
	s.got = append([]string(nil), params.Magnets...)
	if s.items != nil {
		return &store.CheckMagnetData{Items: s.items}, nil
	}
	items := make([]store.CheckMagnetDataItem, 0, len(params.Magnets))
	for _, raw := range params.Magnets {
		magnet, err := core.ParseMagnetLink(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, store.CheckMagnetDataItem{Hash: strings.ToUpper(magnet.Hash), Status: store.MagnetStatusCached})
	}
	return &store.CheckMagnetData{Items: items}, nil
}

type singleHashTestStore struct {
	store.Store
	hash string
}

func (s *singleHashTestStore) GetName() store.StoreName { return store.StoreNameTorrin }
func (s *singleHashTestStore) AddMagnet(*store.AddMagnetParams) (*store.AddMagnetData, error) {
	return &store.AddMagnetData{Hash: s.hash}, nil
}
func (s *singleHashTestStore) GetMagnet(*store.GetMagnetParams) (*store.GetMagnetData, error) {
	return &store.GetMagnetData{Hash: s.hash}, nil
}

func TestCheckMagnetPreservesProviderInput(t *testing.T) {
	testutil.SetupDB(t)
	hash := strings.Repeat("ab", 20)
	fullMagnet := "magnet:?xt=urn:btih:" + strings.ToUpper(hash) + "&dn=Movie.Name"
	provider := &checkMagnetInputTestStore{}
	req := httptest.NewRequest("GET", "http://fixture.invalid", nil)
	req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("test")})

	data, err := checkMagnet(req, &storecontext.Context{Store: provider}, []string{fullMagnet}, "", false)
	require.NoError(t, err)
	require.Equal(t, []string{fullMagnet}, provider.got)
	require.Equal(t, hash, data.Items[0].Hash, "response hashes remain canonical")
}

func TestCheckMagnetDoesNotSilentlyDropInvalidInput(t *testing.T) {
	testutil.SetupDB(t)
	provider := &checkMagnetInputTestStore{}
	req := httptest.NewRequest("GET", "http://fixture.invalid", nil)
	req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("test")})

	_, err := checkMagnet(req, &storecontext.Context{Store: provider}, []string{"not-a-magnet"}, "", false)
	require.Error(t, err)
	require.Equal(t, []string{"not-a-magnet"}, provider.got)
}

func TestCheckMagnetDropsInvalidProviderHashes(t *testing.T) {
	testutil.SetupDB(t)
	hash := strings.Repeat("ab", 20)
	provider := &checkMagnetInputTestStore{items: []store.CheckMagnetDataItem{
		{Hash: strings.ToUpper(hash), Status: store.MagnetStatusCached},
		{Hash: strings.Repeat("a", 64), Status: store.MagnetStatusCached},
		{Hash: "malformed", Status: store.MagnetStatusCached},
	}}
	req := httptest.NewRequest("GET", "http://fixture.invalid", nil)
	req = server.SetReqCtx(req, &server.ReqCtx{Log: logger.Scoped("test")})

	data, err := checkMagnet(req, &storecontext.Context{Store: provider}, []string{hash}, "", false)
	require.NoError(t, err)
	require.Equal(t, []store.CheckMagnetDataItem{{Hash: hash, Status: store.MagnetStatusCached}}, data.Items)
}

func (s *cachedListHashTestStore) GetName() store.StoreName {
	if s.name != "" {
		return s.name
	}
	return store.StoreNameTorrin
}
func (s *cachedListHashTestStore) ListMagnets(*store.ListMagnetsParams) (*store.ListMagnetsData, error) {
	return s.data, nil
}
func TestListMagnetsNormalizesPrivateCopyOfCachedItems(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	cached := &store.ListMagnetsData{Items: []store.ListMagnetsDataItem{{Hash: strings.ToUpper(hash), Id: "CaseSensitiveID", Name: "Movie.MKV"}}, TotalItems: 1}
	result, err := listMagnets(&storecontext.Context{Store: &cachedListHashTestStore{data: cached}}, httptest.NewRequest("GET", "http://fixture.invalid/magnets", nil))
	require.NoError(t, err)
	require.Equal(t, hash, result.Items[0].Hash)
	require.Equal(t, "CaseSensitiveID", result.Items[0].Id)
	require.Equal(t, "Movie.MKV", result.Items[0].Name)
	require.Equal(t, strings.ToUpper(hash), cached.Items[0].Hash)
	result.Items[0].Hash = "changed"
	require.Equal(t, strings.ToUpper(hash), cached.Items[0].Hash)
}

func TestListMagnetsPreservesV2AndDropsMalformedProviderHashes(t *testing.T) {
	testutil.SetupDB(t)
	v1 := strings.ToUpper(testutil.Hash(t))
	v2 := strings.Repeat("aB", 32)
	cached := &store.ListMagnetsData{Items: []store.ListMagnetsDataItem{
		{Hash: v1, Id: "v1", Name: "V1 Movie"},
		{Hash: v2, Id: "v2", Name: "V2 Movie"},
		{Hash: "malformed", Id: "invalid", Name: "Invalid Movie"},
	}, TotalItems: 3}
	result, err := listMagnets(&storecontext.Context{Store: &cachedListHashTestStore{data: cached, name: store.StoreNameRealDebrid}}, httptest.NewRequest("GET", "http://fixture.invalid/magnets", nil))
	require.NoError(t, err)
	require.Equal(t, []store.ListMagnetsDataItem{{Hash: strings.ToLower(v1), Id: "v1", Name: "V1 Movie"}, {Hash: v2, Id: "v2", Name: "V2 Movie"}}, result.Items)
	require.Equal(t, 3, result.TotalItems)
	require.Len(t, cached.Items, 3, "filtering must not mutate an adapter-owned cached slice")
	require.Eventually(t, func() bool {
		var count int
		return db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", strings.ToLower(v1)).Scan(&count) == nil && count == 1
	}, time.Second, 10*time.Millisecond, "wait for asynchronous account-list recording")
	for _, hash := range []string{v2, "malformed"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", hash).Scan(&count))
		require.Zero(t, count, "unsupported account-list hashes must not enter v1 persistence")
	}
}

func TestSingleMagnetProviderHashPolicy(t *testing.T) {
	testutil.SetupDB(t)
	v1 := strings.Repeat("aB", 20)
	v2 := strings.Repeat("aB", 32)
	for _, tc := range []struct {
		name    string
		hash    string
		addErr  bool
		getErr  bool
		getHash string
	}{
		{name: "v1", hash: v1, getHash: strings.ToLower(v1)},
		{name: "v2", hash: v2, addErr: true, getHash: v2},
		{name: "malformed", hash: "malformed", addErr: true, getErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &storecontext.Context{Store: &singleHashTestStore{hash: tc.hash}}
			added, addErr := addMagnet(ctx, strings.Repeat("a", 40), nil)
			if tc.addErr {
				require.Error(t, addErr)
				require.Nil(t, added)
			} else {
				require.NoError(t, addErr)
				require.Equal(t, strings.ToLower(v1), added.Hash)
			}

			got, getErr := getMagnet(ctx, "fixture")
			if tc.getErr {
				require.Error(t, getErr)
				require.Nil(t, got)
			} else {
				require.NoError(t, getErr)
				require.Equal(t, tc.getHash, got.Hash)
			}
			if tc.name == "v2" {
				for _, table := range []struct {
					name   string
					column string
				}{
					{name: "torrent_info", column: "hash"},
					{name: "torrent_stream", column: "h"},
					{name: "magnet_cache", column: "hash"},
				} {
					var count int
					require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table.name+" WHERE "+table.column+" = ?", v2).Scan(&count))
					require.Zero(t, count, "v2 get responses must remain outside v1 persistence")
				}
			}
		})
	}
}
