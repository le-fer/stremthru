package pikpak

import (
	"encoding/base32"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGetFileByMagnetHashUsesCanonicalExactHash(t *testing.T) {
	testutil.SetupDB(t)
	hash := strings.Repeat("ab", 20)
	decoded, err := hex.DecodeString(hash)
	require.NoError(t, err)
	base32Hash := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded)
	for _, tc := range []struct{ name, storedURL, requestedHash string }{
		{"uppercase hex", "magnet:?xt=urn:btih:" + strings.ToUpper(hash), hash},
		{"uppercase request", "magnet:?xt=urn:btih:" + hash, strings.ToUpper(hash)},
		{"base32", "magnet:?xt=urn:btih:" + base32Hash, hash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewStoreClient(&StoreClientConfig{HTTPClient: &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v1/shield/captcha/init":
					return testutil.JSONResponse(r, 200, map[string]any{"captcha_token": "fixture", "expires_in": 86400}), nil
				case "/v1/auth/signin":
					return testutil.JSONResponse(r, 200, map[string]any{"access_token": "fixture", "refresh_token": "fixture", "sub": "fixture", "expires_in": 86400}), nil
				case "/drive/v1/files":
					if r.URL.Query().Get("parent_id") == "" {
						return testutil.JSONResponse(r, 200, map[string]any{"files": []map[string]any{{"id": "pack", "name": "My Pack"}}}), nil
					}
					return testutil.JSONResponse(r, 200, map[string]any{"files": []map[string]any{
						{"id": "wrong-topic", "params": map[string]any{"url": "magnet:?xt=urn:btih:" + strings.Repeat("cd", 20) + "&dn=" + hash}},
						{"id": "not-magnet", "params": map[string]any{"url": "https://fixture.invalid/" + hash}},
						{"id": "existing", "params": map[string]any{"url": tc.storedURL}},
					}}), nil
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					return testutil.JSONResponse(r, 404, map[string]any{}), nil
				}
			})}})
			ctx := Ctx{}
			ctx.APIKey = "fixture:review"
			file, err := client.getFileByMagnetHash(ctx, tc.requestedHash)
			require.NoError(t, err)
			require.NotNil(t, file)
			require.Equal(t, "existing", file.Id)
		})
	}
}
