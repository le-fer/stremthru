package testutil

import (
	"net/http"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/stretchr/testify/require"
)

// ObserveMagnetTracking waits for the last outbound tracking operation so serial
// fixtures can restore shared clients and settings after BulkTrackMagnet reads them.
func ObserveMagnetTracking(t *testing.T) func() {
	t.Helper()
	previousBuddy, previousPeer := buddy.Buddy, buddy.Peer
	previousHasBuddy, previousHasPeer, previousToken := config.HasBuddy, config.HasPeer, config.PeerAuthToken
	config.HasBuddy, config.HasPeer, config.PeerAuthToken = true, true, "fixture-token"
	tracked := make(chan struct{}, 1)
	buddy.Buddy = buddy.NewAPIClient(&buddy.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return JSONResponse(r, 200, map[string]any{"data": map[string]any{}}), nil
	})}})
	buddy.Peer = peer.NewAPIClient(&peer.APIClientConfig{BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		tracked <- struct{}{}
		return JSONResponse(r, 200, map[string]any{"data": map[string]any{}}), nil
	})}})
	t.Cleanup(func() {
		buddy.Buddy, buddy.Peer = previousBuddy, previousPeer
		config.HasBuddy, config.HasPeer, config.PeerAuthToken = previousHasBuddy, previousHasPeer, previousToken
	})
	return func() {
		t.Helper()
		select {
		case <-tracked:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "magnet tracking did not complete")
		}
	}
}
