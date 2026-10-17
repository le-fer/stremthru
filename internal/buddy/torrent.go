package buddy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/peer"
	ti "github.com/MunifTanjim/stremthru/internal/torrent_info"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tss "github.com/MunifTanjim/stremthru/internal/torrent_stream/torrent_stream_syncinfo"
)

var PullPeer, pullLocalOnly = func() (*peer.APIClient, bool) {
	baseUrl := config.PullPeerURL
	if baseUrl == "" {
		baseUrl = config.PeerURL
	}
	localOnly := baseUrl == config.PullPeerURL
	if baseUrl == "" {
		return nil, localOnly
	}
	return peer.NewAPIClient(&peer.APIClientConfig{
		BaseURL: baseUrl,
	}), localOnly
}()

var pullPeerLog = logger.Scoped("peer:pull")

var noTorrentInfo = !config.Feature.HasTorz()

// supports imdb or anidb
func PullTorrentsByStremId(sid string, originInstanceId string) []string {
	hashes, _ := pullTorrentsByStremId(sid, originInstanceId)
	return hashes
}

// Peer exports must distinguish a failed import from a successful empty pull.
// Other callers retain the best-effort pull API above.
func pullTorrentsByStremId(sid string, originInstanceId string) ([]string, error) {
	if noTorrentInfo || PullPeer == nil || !tss.ShouldPull(sid) {
		return nil, nil
	}
	if PullPeer.IsHaltedCheckMagnet() {
		return nil, core.NewUpstreamError("peer torrent pull temporarily halted")
	}

	cleanSId := ts.CleanStremId(sid)
	start := time.Now()
	res, err := PullPeer.ListTorrents(&peer.ListTorrentsByStremIdParams{
		SId:              cleanSId,
		LocalOnly:        pullLocalOnly,
		OriginInstanceId: originInstanceId,
	})
	fetchDuration := time.Since(start)

	if err != nil {
		if fetchDuration > 25*time.Second {
			PullPeer.HaltCheckMagnet()
		}

		// Transport errors and peer response bodies can contain private URLs or
		// credentials. Keep their text and causes out of logs and public errors.
		var netErr net.Error
		timeout := errors.As(err, &netErr) && netErr.Timeout()
		statusCode := res.StatusCode
		var transportErr *url.Error
		if errors.As(err, &transportErr) {
			// The API response wrapper substitutes 503 when no HTTP response exists.
			statusCode = 0
		}
		category := "transport"
		var dnsErr *net.DNSError
		switch {
		case timeout:
			category = "timeout"
		case errors.Is(err, context.Canceled):
			category = "canceled"
		case errors.As(err, &dnsErr):
			category = "dns"
		case statusCode >= 400:
			category = "http"
		case statusCode != 0:
			category = "response"
		}
		pullPeerLog.Error("failed to pull torrents", "error_type", fmt.Sprintf("%T", err), "error_category", category, "status_code", statusCode, "timeout", timeout, "fetch_duration", fetchDuration, "duration", time.Since(start), "sid", cleanSId)
		return nil, core.NewUpstreamError("failed to pull peer torrents")
	}

	count := len(res.Data.Items)
	importStart := time.Now()

	hashes := make([]string, count)
	items := make([]ti.TorrentInfoInsertData, count)
	for i := range res.Data.Items {
		data := &res.Data.Items[i]
		hashes[i] = data.Hash
		items[i] = ti.TorrentInfoInsertData{
			Hash:         data.Hash,
			TorrentTitle: data.TorrentTitle,
			Size:         data.Size,
			Indexer:      data.Indexer,
			Source:       ti.TorrentInfoSource(data.Source),
			Category:     ti.TorrentInfoCategory(data.Category),
			Files:        data.Files,
			Seeders:      data.Seeders,
			Leechers:     data.Leechers,
			Private:      data.Private,
		}
	}
	if err := ti.UpsertFromPeer(items, cleanSId); err != nil {
		pullPeerLog.Error("failed to import pulled torrents", "error", core.PackError(err), "fetch_duration", fetchDuration, "import_duration", time.Since(importStart), "duration", time.Since(start), "sid", cleanSId)
		return nil, err
	}
	importDuration := time.Since(importStart)
	tss.MarkPulled(cleanSId)
	pullPeerLog.Info("pulled torrents", "fetch_duration", fetchDuration, "import_duration", importDuration, "duration", time.Since(start), "sid", cleanSId, "count", count)
	return hashes, nil
}

func ListTorrentsByStremId(sid string, localOnly bool, originInstanceId string, noMissingSize bool) (*ti.ListTorrentsData, error) {
	if originInstanceId == config.InstanceId && !pullLocalOnly {
		pullPeerLog.Info("loop detected for list torrents, self-correcting...")
		pullLocalOnly = true
	}

	if !localOnly {
		if _, err := pullTorrentsByStremId(sid, originInstanceId); err != nil {
			return nil, err
		}
	}

	// Export committed imports even while replicas lag, including requests
	// served from the pull cooldown or restricted to local data.
	data, err := ti.ListByStremIdFromPrimary(sid, noMissingSize)
	if err != nil {
		return nil, err
	}
	return data, nil
}
