package stremio_store

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/realdebrid"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/stretchr/testify/require"
)

type sampleContentStore struct {
	store.Store
	store.WebzStore
	name  store.StoreName
	files []store.WebzFile
}

func (s sampleContentStore) GetName() store.StoreName { return s.name }
func (s sampleContentStore) GetWebz(*store.GetWebzParams) (*store.GetWebzData, error) {
	return &store.GetWebzData{Id: "fixture", Files: s.files}, nil
}

func TestWebzContentPreservesSamplePaths(t *testing.T) {
	for _, provider := range []store.StoreName{store.StoreNameDebridLink, store.StoreNamePikPak} {
		t.Run(string(provider), func(t *testing.T) {
			files := []store.WebzFile{
				{Idx: 0, Name: "Example.Series.S01E02.mkv", Path: "/Example.Series/Sample/Example.Series.S01E02.mkv"},
				{Idx: 1, Name: "Example.Series.S01E02.mkv", Path: "/Example.Series/Example.Series.S01E02.mkv"},
			}
			info, err := getStoreContentInfo(sampleContentStore{name: provider, files: files}, "token", "fixture", "", &ParsedId{isWebDL: true})
			require.NoError(t, err)
			require.Len(t, info.Files, 2)
			for i, f := range info.Files {
				require.Equal(t, files[i].Path, f.Path)
				require.Equal(t, i == 0, core.IsSampleFile(f.Name, f.Path))
			}
		})
	}
}

type sampleDownloadTransport func(*http.Request) (*http.Response, error)

func (f sampleDownloadTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRDWebDownloadsExcludeSamples(t *testing.T) {
	oldClient := rdClient
	t.Cleanup(func() { rdClient = oldClient })
	const response = `[
		{"id":"sample","filename":"Example.Series.S01E02-sample.mkv","host":"fixture.invalid","download":"https://fixture.invalid/sample"},
		{"id":"full","filename":"Example.Series.S01E02.mkv","host":"fixture.invalid","download":"https://fixture.invalid/full"}
	]`
	rdClient = realdebrid.NewAPIClient(&realdebrid.APIClientConfig{HTTPClient: &http.Client{Transport: sampleDownloadTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/rest/1.0/downloads", r.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})}})
	ctx := &Ctx{Log: logger.Scoped("sample-test")}
	ctx.Store = sampleContentStore{name: store.StoreNameRealDebrid}
	ctx.StoreAuthToken = "sample-regression-fixture"
	idr := &ParsedId{storeCode: store.StoreCodeRealDebrid, storeName: store.StoreNameRealDebrid, isWebDL: true}
	cacheKey := getRDDownloadsCacheKey(idr.getStoreCode(), ctx.StoreAuthToken)
	rdDownloadsCache.Remove(cacheKey)
	t.Cleanup(func() { rdDownloadsCache.Remove(cacheKey) })
	meta := getRDWebDLsMeta(httptest.NewRequest(http.MethodGet, "https://fixture.invalid/", nil), ctx, idr)
	require.Len(t, meta.Videos, 1)
	require.NotContains(t, meta.Videos[0].Title, "sample")
	require.Len(t, meta.Videos[0].Streams, 1)
	require.Equal(t, "Example.Series.S01E02.mkv", meta.Videos[0].Streams[0].BehaviorHints.Filename)
	require.Equal(t, "https://fixture.invalid/full", meta.Videos[0].Streams[0].URL)
}

func TestCatalogsExcludeNamedSamplesWithoutMutatingCache(t *testing.T) {
	for _, kind := range []string{"torrent", "usenet", "webdl"} {
		t.Run(kind, func(t *testing.T) {
			idr := &ParsedId{storeCode: store.StoreCodeRealDebrid, storeName: store.StoreNameRealDebrid, isUsenet: kind == "usenet", isWebDL: kind == "webdl"}
			token := t.TempDir()
			key := getCatalogCacheKey(idr.getStoreCode(), token)
			items := []CachedCatalogItem{
				{MetaPreview: stremio.MetaPreview{Id: "sample", Name: "Example.Series.S02E03-sample.mkv"}},
				{MetaPreview: stremio.MetaPreview{Id: "full", Name: "Example.Series.S02E03.mkv"}},
				{MetaPreview: stremio.MetaPreview{Id: "title-word", Name: "Sample.Title.2024.1080p.mkv"}},
			}
			require.NoError(t, catalogCache.Add(key, items))
			t.Cleanup(func() { catalogCache.Remove(key) })
			result := getCatalogItems(sampleContentStore{name: store.StoreNameRealDebrid}, token, "", idr, logger.Scoped("sample-test"))
			require.Len(t, result, 2)
			require.Equal(t, "full", result[0].Id)
			require.Equal(t, "title-word", result[1].Id)
			var retained []CachedCatalogItem
			require.True(t, catalogCache.Get(key, &retained))
			require.Len(t, retained, 3)
		})
	}
}
