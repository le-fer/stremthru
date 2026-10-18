package stremio_store_usenet

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/torbox"
	"github.com/stretchr/testify/require"
)

func TestNewsLargestFileNameExcludesSamples(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []NewsFile
		want  string
	}{
		{"larger sample", []NewsFile{{Name: "series.s01e03-sample.mkv", Size: 30}, {Name: "series.s01e03.mkv", Size: 20}}, "series.s01e03.mkv"},
		{"folder sample", []NewsFile{{Name: "E03.mkv", Path: "/Release/Sample/E03.mkv", Size: 30}, {Name: "episode.mkv", Size: 20}}, "episode.mkv"},
		{"sample and metadata only", []NewsFile{{Name: "sample.mkv", Size: 20}, {Name: "release.nfo", Size: 30}}, ""},
		{"larger metadata", []NewsFile{{Name: "release.nfo", Size: 30}, {Name: "episode.mkv", Size: 20}}, "episode.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := News{Files: tc.files}
			require.Equal(t, tc.want, n.GetLargestFileName())
			require.Equal(t, tc.files, n.Files, "display selection must preserve file evidence")
		})
	}
}

type sampleTestTransport func(*http.Request) (*http.Response, error)

func (f sampleTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewsGarbageNameReplacementExcludesSamples(t *testing.T) {
	previousClient := tbClient
	t.Cleanup(func() { tbClient = previousClient })
	for _, tc := range []struct {
		name  string
		files []torbox.UsenetDownloadFile
		want  string
	}{
		{"mixed files", []torbox.UsenetDownloadFile{
			{Id: 1, Name: "Release/series.s01e03-sample.mkv", ShortName: "series.s01e03-sample.mkv", Size: 40},
			{Id: 2, Name: "Release/Sample/E03.mkv", ShortName: "E03.mkv", Size: 30},
			{Id: 3, Name: "Release/release.nfo", ShortName: "release.nfo", Size: 50},
			{Id: 4, Name: "Release/episode.mkv", ShortName: "episode.mkv", Size: 20},
		}, "episode.mkv"},
		{"sample and metadata only", []torbox.UsenetDownloadFile{
			{Id: 1, Name: "Release/Sample/E03.mkv", ShortName: "E03.mkv", Size: 20},
			{Id: 2, Name: "Release/release.nfo", ShortName: "release.nfo", Size: 30},
		}, "[abc123] - def456"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := torbox.UsenetDownload{Id: 123, Name: "[abc123] - def456", Files: tc.files}
			tbClient = torbox.NewAPIClient(&torbox.APIClientConfig{HTTPClient: &http.Client{Transport: sampleTestTransport(func(r *http.Request) (*http.Response, error) {
				var data any = []torbox.UsenetDownload{fixture}
				if r.URL.Query().Get("id") != "" {
					data = fixture
				}
				body, err := json.Marshal(map[string]any{"success": true, "data": data})
				require.NoError(t, err)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})}})
			listed, err := ListNews(&ListNewsParams{Limit: 10}, store.StoreNameTorBox)
			require.NoError(t, err)
			require.Len(t, listed.Items, 1)
			got, err := GetNews(&GetNewsParams{Id: "123"}, store.StoreNameTorBox)
			require.NoError(t, err)
			for _, n := range []News{listed.Items[0], *got} {
				require.Equal(t, tc.want, n.Name)
				require.Len(t, n.Files, len(tc.files), "display filtering must preserve provider evidence")
				for i, f := range n.Files {
					require.Equal(t, tc.files[i].ShortName, f.Name)
					require.Equal(t, "/"+tc.files[i].Name, f.Path)
				}
			}
		})
	}
}
