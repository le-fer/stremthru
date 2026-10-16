package stremio_wrap

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/cache"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/server"
	"github.com/MunifTanjim/stremthru/internal/shared"
	store_video "github.com/MunifTanjim/stremthru/internal/store/video"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	stremio_store "github.com/MunifTanjim/stremthru/internal/stremio/store"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"golang.org/x/sync/singleflight"
)

var stremLinkCache = cache.NewCache[string](&cache.CacheConfig{
	Name:     "stremio:wrap:streamLink",
	Lifetime: 3 * time.Hour,
})

func redirectToStaticVideo(w http.ResponseWriter, r *http.Request, cacheKey string, videoName string) {
	url := store_video.Redirect(videoName, w, r)
	stremLinkCache.AddWithLifetime(cacheKey, url, 1*time.Minute)
}

var stremGroup singleflight.Group

type stremResult struct {
	link        string
	error_level logger.Level
	error_log   string
	error_video string
}

func handleStrem(w http.ResponseWriter, r *http.Request) {
	if !IsMethod(r, http.MethodGet) && !IsMethod(r, http.MethodHead) {
		shared.ErrorMethodNotAllowed(r).Send(w, r)
		return
	}

	log := server.GetReqCtx(r).Log

	magnetHash := r.PathValue("magnetHash")
	magnetHash = core.NormalizeMagnetHash(magnetHash)
	fileName := r.PathValue("fileName")
	fileIdx := -1
	if idx, err := strconv.Atoi(r.PathValue("fileIdx")); err == nil {
		fileIdx = idx
	}

	ud, err := getUserData(r)
	if err != nil {
		SendError(w, r, err)
		return
	}

	ctx, err := ud.GetRequestContext(r)
	if err != nil {
		LogError(r, "failed to get request context", err)
		shared.ErrorBadRequest(r, "failed to get request context: "+err.Error()).Send(w, r)
		return
	}

	query := r.URL.Query()
	sid := query.Get("sid")
	if sid == "" {
		sid = "*"
	}

	s := ud.GetStoreByCode(query.Get("s"))
	ctx.Store, ctx.StoreAuthToken = s.Store, s.AuthToken
	storeCode := ctx.Store.GetName().Code()
	fileRequest := stremio_shared.PlaybackFileRequest{Name: fileName, Index: fileIdx, StoreCode: storeCode, AllowMixed: config.AllowVideoTorrentsWithDisallowedFiles}

	cacheKey := strings.Join([]string{ctx.ClientIP, string(storeCode), ctx.StoreAuthToken, magnetHash, strconv.Itoa(fileIdx), fileName, query.Encode()}, ":")

	blocked, err := stremio_shared.StoredPlaybackIsBlocked(magnetHash, fileRequest)
	if err != nil {
		log.Error("failed to check retained playback evidence", "error", err, "hash", magnetHash, "sid", sid, "store.name", ctx.Store.GetName())
		// Do not cache a transient database failure under the playback key.
		redirectToStaticVideo(w, r, "", store_video.StoreVideoName500)
		return
	}
	if blocked {
		redirectToStaticVideo(w, r, cacheKey, store_video.StoreVideoNameNoMatchingFile)
		return
	}

	stremLink := ""
	if stremLinkCache.Get(cacheKey, &stremLink) {
		log.Debug("redirecting to cached stream link")
		http.Redirect(w, r, stremLink, http.StatusFound)
		return
	}

	result, err, _ := stremGroup.Do(cacheKey, func() (any, error) {
		log.Debug("creating stream link")
		amParams := &store.AddMagnetParams{
			Magnet:   magnetHash,
			ClientIP: ctx.ClientIP,
		}
		amParams.APIKey = ctx.StoreAuthToken
		amRes, err := ctx.Store.AddMagnet(amParams)
		if err != nil {
			return &stremResult{
				error_level: logger.LevelError,
				error_log:   "failed to add magnet",
				error_video: "download_failed",
			}, err
		}

		stremio_store.InvalidateCatalogCache(storeCode, ctx.StoreAuthToken)

		magnet := &store.GetMagnetData{
			Id:      amRes.Id,
			Name:    amRes.Name,
			Hash:    amRes.Hash,
			Size:    amRes.Size,
			Status:  amRes.Status,
			Files:   amRes.Files,
			Private: amRes.Private,
			AddedAt: amRes.AddedAt,
		}

		magnet, err = stremio_shared.WaitForMagnetStatus(ctx, magnet, store.MagnetStatusDownloaded, 3, 5*time.Second)
		if err != nil {
			strem := &stremResult{
				error_level: logger.LevelError,
				error_log:   "failed wait for magnet status",
				error_video: "500",
			}
			if magnet.Status == store.MagnetStatusQueued || magnet.Status == store.MagnetStatusDownloading || magnet.Status == store.MagnetStatusProcessing {
				strem.error_level = logger.LevelWarn
				strem.error_video = "downloading"
			} else if magnet.Status == store.MagnetStatusFailed || magnet.Status == store.MagnetStatusInvalid || magnet.Status == store.MagnetStatusUnknown {
				strem.error_level = logger.LevelWarn
				strem.error_video = "download_failed"
			}
			return strem, err
		}

		var pattern *regexp.Regexp
		if re := query.Get("re"); re != "" {
			if pat, err := regexp.Compile(re); err == nil {
				pattern = pat
			}
		}

		shouldTagStream := strings.HasPrefix(sid, "tt") && !storeCode.HasUntrustedData()

		rejectFiles := func(reason string) (*stremResult, error) {
			buddy.TrackMagnet(ctx.Store, magnet.Hash, magnet.Name, magnet.Size, magnet.Private, magnet.Files, torrent_info.GetCategoryFromStremId(sid, ""), magnet.Status != store.MagnetStatusDownloaded, ctx.StoreAuthToken)
			return &stremResult{error_level: logger.LevelWarn, error_log: reason + " (" + sid + " - " + magnet.Hash + ")", error_video: store_video.StoreVideoNameNoMatchingFile}, nil
		}
		videoFiles, validationErr := stremio_shared.ValidatePlaybackFiles(magnet.Files, fileRequest)
		if validationErr != nil {
			return rejectFiles(validationErr.Error())
		}

		var file store.File
		if fileName != "" {
			if file = stremio_shared.MatchFileByName(videoFiles, fileName); file != nil {
				log.Debug("matched file using filename", "filename", file.GetName())
			}
		}
		if file == nil && strings.Contains(sid, ":") {
			if file = stremio_shared.MatchFileByStremId(magnet.Name, videoFiles, sid, magnetHash, storeCode); file != nil {
				log.Debug("matched file using stream id", "sid", sid, "filename", file.GetName())
			}
		}
		if file == nil {
			if file = stremio_shared.MatchFileByIdx(videoFiles, fileIdx, storeCode); file != nil {
				log.Debug("matched file using fileidx", "fileidx", file.GetIdx(), "filename", file.GetName())
			}
		}
		if file == nil && pattern != nil {
			if file = stremio_shared.MatchFileByPattern(videoFiles, pattern); file != nil {
				log.Debug("matched file using pattern", "pattern", pattern.String(), "filename", file.GetName())
			}
		}
		if file == nil {
			if file = stremio_shared.MatchFileByLargestSize(videoFiles); file != nil {
				log.Debug("matched file using largest size", "filename", file.GetName())
				shouldTagStream = false
			}
		}

		go func() {
			buddy.TrackMagnet(ctx.Store, magnet.Hash, magnet.Name, magnet.Size, magnet.Private, magnet.Files, torrent_info.GetCategoryFromStremId(sid, ""), magnet.Status != store.MagnetStatusDownloaded, ctx.StoreAuthToken)
			if shouldTagStream && file != nil {
				torrent_stream.TagStremId(magnet.Hash, file.GetPath(), sid)
			}
		}()

		link := ""
		if file != nil {
			link = file.GetLink()
		}
		if link == "" {
			return &stremResult{
				error_level: logger.LevelWarn,
				error_log:   "no matching file found for (" + sid + " - " + magnet.Hash + ")",
				error_video: "no_matching_file",
			}, nil
		}

		glRes, err := shared.GenerateStremThruLink(r, &ctx.Context, link, fileName)
		if err != nil {
			return &stremResult{
				error_level: logger.LevelError,
				error_log:   "failed to generate stremthru link",
				error_video: "500",
			}, err
		}

		stremLinkCache.Add(cacheKey, glRes.Link)

		if storeCode == store.StoreCodeTorBox {
			torrent_stream.QueueMediaInfoProbe(magnet.Hash, file.GetPath(), glRes.Link)
		} else if storeCode == store.StoreCodeRealDebrid && glRes.LinkId != "" {
			torrent_stream.QueueStoreMediaInfoProbe(magnet.Hash, file.GetPath(), string(store.StoreCodeRealDebrid), ctx.StoreAuthToken, glRes.LinkId)
		}

		return &stremResult{
			link: glRes.Link,
		}, nil
	})

	strem := result.(*stremResult)

	if strem.error_log != "" {
		log.Log(strem.error_level, strem.error_log, "error", err, "store.name", ctx.Store.GetName())
		redirectToStaticVideo(w, r, cacheKey, strem.error_video)
		return
	}

	log.Debug("redirecting to stream link")
	http.Redirect(w, r, strem.link, http.StatusFound)
}
