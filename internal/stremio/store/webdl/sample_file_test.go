package stremio_store_webdl

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/alldebrid"
	"github.com/MunifTanjim/stremthru/store/premiumize"
	"github.com/stretchr/testify/require"
)

type sampleLinkTransport func(*http.Request) (*http.Response, error)

func (f sampleLinkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGenerateLinkExcludesSampleFiles(t *testing.T) {
	oldAD, oldPM := adClient, pmClient
	t.Cleanup(func() { adClient, pmClient = oldAD, oldPM })
	for _, provider := range []store.StoreName{store.StoreNameAlldebrid, store.StoreNamePremiumize} {
		for _, sample := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/sample=%t", provider, sample), func(t *testing.T) {
				name := "Example.Series.S01E02.mkv"
				if sample {
					name = "Example.Series.S01E02-sample.mkv"
				}
				calls := 0
				client := &http.Client{Transport: sampleLinkTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					body := ""
					if provider == store.StoreNameAlldebrid {
						require.Equal(t, "/v4/link/unlock", r.URL.Path)
						body = fmt.Sprintf(`{"status":"success","data":{"filename":%q,"link":"https://fixture.invalid/video"}}`, name)
					} else {
						require.Equal(t, "/item/details", r.URL.Path)
						body = fmt.Sprintf(`{"status":"success","name":%q,"link":"https://fixture.invalid/video"}`, name)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}
				adClient = alldebrid.NewAPIClient(&alldebrid.APIClientConfig{HTTPClient: client})
				pmClient = premiumize.NewAPIClient(&premiumize.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: client})
				data, err := GenerateLink(&GenerateLinkParams{Link: "fixture"}, provider)
				require.Equal(t, 1, calls)
				if sample {
					require.Nil(t, data)
					var apiErr *core.APIError
					require.ErrorAs(t, err, &apiErr)
					require.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
				} else {
					require.NoError(t, err)
					require.Equal(t, "https://fixture.invalid/video", data.Link)
				}
			})
		}
	}
}
