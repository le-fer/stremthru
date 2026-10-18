package stremio_store

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	"github.com/MunifTanjim/stremthru/internal/server"
	"github.com/MunifTanjim/stremthru/internal/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/torrin"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type seasonRoundTripFunc func(*http.Request) (*http.Response, error)

func (f seasonRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the real handlers through an in-memory provider transport. Torrin
// disables recording provider metadata, so no background writes outlive cleanup.
func storeSeasonFixture(t *testing.T, torrentName, filePath, sid, title, original string) func(string, string) *httptest.ResponseRecorder {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	db.SetForTesting(sqlDB, t.Cleanup)
	hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
	if sid != "" {
		titleID, _, _ := strings.Cut(sid, ":")
		titleType, metaType := "tvSeries", "series"
		if !strings.Contains(sid, ":") {
			titleType, metaType = "movie", "movie"
		}
		require.NoError(t, imdb_title.Upsert([]imdb_title.IMDBTitle{{TId: titleID, Title: title, OrigTitle: original, Type: titleType}}))
		require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/association.txt", SId: sid, Source: "tor"}}}, false))
		key := metaType + ":" + titleID
		require.NoError(t, metaCache.Add(key, stremio.MetaHandlerResponse{Meta: stremio.Meta{Id: titleID, Type: stremio.ContentType(metaType), Name: title}}))
		t.Cleanup(func() { metaCache.Remove(key) })
	}
	magnet := store.GetMagnetData{Id: hash, Hash: hash, Name: torrentName, Status: store.MagnetStatusDownloaded, Files: []store.MagnetFile{{Name: path.Base(filePath), Path: filePath, Idx: 1, Link: "https://files.invalid/selected", Size: 100}}}
	payload, err := json.Marshal(map[string]any{"data": magnet})
	require.NoError(t, err)
	provider := shared.GetStore(string(store.StoreNameTorrin)).(*torrin.StoreClient)
	previous := *provider
	*provider = *torrin.NewStoreClient(&torrin.StoreClientConfig{BaseURL: "https://provider.invalid", HTTPClient: &http.Client{Transport: seasonRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "provider.invalid" || r.Method != http.MethodGet || r.URL.Path != "/v0/store/magnets/"+hash {
			return nil, fmt.Errorf("unexpected mock request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload))), Request: r}, nil
	})}})
	t.Cleanup(func() { *provider = previous })
	ud := UserData{StoreName: string(store.StoreNameTorrin), StoreToken: hash}
	require.NoError(t, udManager.Sync(&ud))
	code := string(store.StoreNameTorrin.Code())
	// Avoid catalog fetching and its background metadata recording in stream tests.
	catalogKey := getCatalogCacheKey(code, hash)
	require.NoError(t, catalogCache.Add(catalogKey, []CachedCatalogItem{{MetaPreview: stremio.MetaPreview{Id: getIdPrefix(code) + hash, Name: title}, Hash: hash}}))
	t.Cleanup(func() { catalogCache.Remove(catalogKey) })
	return func(kind, requestID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://addon.invalid/stremio/store/test/"+kind, nil)
		req.SetPathValue("userData", ud.GetEncoded())
		req.SetPathValue("contentType", ContentTypeOther)
		req.SetPathValue("id", getIdPrefix(code)+hash)
		if kind == "stream" {
			req.SetPathValue("contentType", "series")
			req.SetPathValue("id", requestID)
		}
		req = server.SetReqCtx(req, &server.ReqCtx{Log: log})
		response := httptest.NewRecorder()
		if kind == "meta" {
			handleMeta(response, req)
		} else {
			handleStream(response, req)
		}
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		return response
	}
}

func TestStoreMetadataSeasonEvidence(t *testing.T) {
	const sid = "tt9998701:3:10"
	for _, tc := range []struct {
		name, torrent, filePath, sid, title, original string
		want                                          int
	}{
		{"title in parent", "Synthetic Series Season 2", "/Synthetic.Series.Season.2/Season 3/E10.mkv", sid, "Synthetic Series Season 2", "", 3},
		{"title in filename", "Synthetic Series Season 2", "/Season 3/Synthetic.Series.Season.2.E10.mkv", sid, "Synthetic Series Season 2", "", 3},
		{"original title", "Synthetic Series S03", "/S03/Synthetic.Original.Sezonul.2/E10.mkv", sid, "Synthetic Series Season 2", "Synthetic Original Sezonul 2", 3},
		{"aspect ratio is unknown", "Synthetic.Series.16x9", "/Synthetic Series/E10.mkv", "", "", "", -1},
		{"missing evidence", "Synthetic Series Complete", "/Synthetic Series/E10.mkv", "", "", "", -1},
		{"single season release", "Synthetic Series Season 3 1080p", "/Synthetic Series/E10.mkv", "", "", "", 3},
		{"conflicting folders", "Synthetic Series S03", "/S02/Season 3/E10.mkv", "", "", "", -1},
		{"movie preserves filename parsing", "Synthetic Movie", "/Season 3/Synthetic.Movie.Season.2.mkv", "tt9998702", "Synthetic Movie", "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := storeSeasonFixture(t, tc.torrent, tc.filePath, tc.sid, tc.title, tc.original)
			response := run("meta", "")
			var result struct {
				Meta struct {
					Videos []struct {
						Season *int `json:"season"`
					} `json:"videos"`
				} `json:"meta"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Len(t, result.Meta.Videos, 1)
			want := tc.want
			if tc.original != "" && !config.Feature.HasIMDBTitle() {
				want = -1
			}
			if want == -1 {
				require.Nil(t, result.Meta.Videos[0].Season, response.Body.String())
			} else {
				require.NotNil(t, result.Meta.Videos[0].Season, response.Body.String())
				require.Equal(t, want, *result.Meta.Videos[0].Season)
			}
		})
	}
}

func TestStoreStreamProtectsOriginalTitle(t *testing.T) {
	const sid = "tt9998703:3:10"
	run := storeSeasonFixture(t, "Synthetic Series S03", "/S03/Synthetic.Original.Sezonul.2/E10.mkv", sid, "Synthetic Series Season 2", "Synthetic Original Sezonul 2")
	response := run("stream", sid)
	var result stremio.StreamHandlerResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	if !config.Feature.HasIMDBTitle() {
		require.Empty(t, result.Streams)
		return
	}
	require.Len(t, result.Streams, 1, response.Body.String())
	require.Equal(t, "E10.mkv", result.Streams[0].BehaviorHints.Filename)
}
