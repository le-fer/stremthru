package torznab_client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

func TestEnsureMagnetNormalizesExistingAndCachedHashes(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	existing := Torz{Hash: strings.ToUpper(hash), MagnetLink: "magnet:?xt=urn:btih:" + hash}
	require.NoError(t, existing.EnsureMagnet(context.Background()))
	require.Equal(t, hash, existing.Hash)
	source := "https://fixture.invalid/" + t.Name()
	torzFileCache.Add(source, torzFileCached{Hash: strings.ToUpper(hash), MagnetLink: existing.MagnetLink, Private: true})
	t.Cleanup(func() { torzFileCache.Remove(source) })
	cached := Torz{SourceLink: source}
	require.NoError(t, cached.EnsureMagnet(context.Background()))
	require.Equal(t, hash, cached.Hash)
	require.True(t, cached.Private)
}

func TestEnsureMagnetRetainsHybridTorrentCompatibility(t *testing.T) {
	info := map[string]any{
		"piece length": int64(16 * 1024),
		"pieces":       make([]byte, 20),
		"name":         "Hybrid",
		"files":        []metainfo.FileInfo{{Length: 1, Path: []string{"movie.mkv"}}},
		"meta version": int64(2),
		"file tree": map[string]any{
			"movie.mkv": map[string]any{
				"": map[string]any{"length": int64(1), "pieces root": make([]byte, 32)},
			},
		},
	}
	infoBytes, err := bencode.Marshal(info)
	require.NoError(t, err)
	meta := metainfo.MetaInfo{InfoBytes: infoBytes}
	var torrent bytes.Buffer
	require.NoError(t, meta.Write(&torrent))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hybrid.torrent" {
			t.Errorf("unexpected fixture request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="hybrid.torrent"`)
		if _, err := w.Write(torrent.Bytes()); err != nil {
			t.Errorf("write torrent fixture: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	source := server.URL + "/hybrid.torrent"
	t.Cleanup(func() { torzFileCache.Remove(source) })
	item := Torz{SourceLink: source}
	require.NoError(t, item.EnsureMagnet(context.Background()))
	require.Equal(t, meta.HashInfoBytes().HexString(), item.Hash, "hybrid torrents remain keyed by their v1 identity")
	require.Len(t, item.Files, 1)
	require.Equal(t, "movie.mkv", item.Files[0].Name)

	magnetURL, err := url.Parse(item.MagnetLink)
	require.NoError(t, err)
	exactTopics := magnetURL.Query()["xt"]
	require.Len(t, exactTopics, 2)
	require.Contains(t, exactTopics, "urn:btih:"+item.Hash)
	hasV2 := false
	for _, topic := range exactTopics {
		hasV2 = hasV2 || strings.HasPrefix(topic, "urn:btmh:1220")
	}
	require.True(t, hasV2, "generated hybrid magnet must retain its v2 exact topic")
}
