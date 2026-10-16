package stremio_wrap

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/shared"
	stremio_addon "github.com/MunifTanjim/stremthru/internal/stremio/addon"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	stremio_torz "github.com/MunifTanjim/stremthru/internal/stremio/torz"
	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	torznab_indexer_syncinfo "github.com/MunifTanjim/stremthru/internal/torznab/indexer/syncinfo"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/internal/worker"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/stremio"
)

var lazyPullTorz = config.Stremio.Torz.LazyPull

func (ud UserData) fetchStream(ctx *Ctx, r *http.Request, rType, id string) (*stremio.StreamHandlerResponse, error) {
	log := ctx.Log

	filter, filter_err := ud.GetFilter()
	if filter_err != nil {
		log.Warn("failed to parse filter expression", "error", filter_err)
		return nil, shared.ErrorBadRequest(r, "invalid filter expression: "+filter_err.Error())
	}

	eud := ud.GetEncoded()

	stremId := strings.TrimSuffix(id, ".json")

	upstreams, err := ud.getUpstreams(ctx, stremio.ResourceNameStream, rType, id)
	if err != nil {
		return nil, err
	}
	upstreamsCount := len(upstreams)
	log.Debug("found addons for stream", "count", upstreamsCount)

	chunksCount := upstreamsCount
	if ud.IncludeTorz {
		chunksCount += 1
	}
	chunks := make([][]WrappedStream, chunksCount)
	errs := make([]error, chunksCount)

	template, err := ud.template.Parse()
	if err != nil {
		return nil, err
	}

	isImdbStremId := strings.HasPrefix(stremId, "tt")
	torrentInfoCategory := torrent_info.GetCategoryFromStremId(stremId, rType)

	nsid, err := torrent_stream.NormalizeStreamId(stremId)
	if err == nil {
		cleanSId := nsid.ToClean()
		if !ud.IncludeTorz || lazyPullTorz {
			go buddy.PullTorrentsByStremId(cleanSId, "")
		} else {
			buddy.PullTorrentsByStremId(cleanSId, "")
		}

		torznab_indexer_syncinfo.QueueJob(nsid.String())
	} else if !errors.Is(err, torrent_stream.ErrUnsupportedStremId) {
		log.Error("failed to normalize strem id", "strem_id", stremId, "error", err)
	} else {
		log.Warn("unsupported strem id for normalization", "strem_id", stremId)
	}

	chunkIdxOffset := 0
	var wg sync.WaitGroup
	if ud.IncludeTorz {
		chunkIdxOffset = 1
		wg.Go(func() {

			hashes, err := torrent_info.ListHashesByStremId(stremId)
			if err != nil {
				if errors.Is(err, torrent_stream.ErrUnsupportedStremId) {
					return
				}

				errs[0] = err
				return
			}

			if nsid == nil {
				return
			}

			streams, err := stremio_torz.GetStreamCandidatesForHashes(rType, stremId, hashes, nsid)
			if err != nil {
				errs[0] = err
				return
			}

			wstreams := make([]WrappedStream, len(streams))
			for i := range streams {
				wstream := &streams[i]
				stream := wstream.Stream
				tmpl := template
				if tmpl == nil || tmpl.IsEmpty() || tmpl.IsRaw() {
					tmpl = stremio_transformer.StreamTemplateDefault
				}
				s, err := tmpl.Execute(stream, wstream.R)
				if err != nil {
					errs[0] = err
					return
				}
				wstreams[i] = WrappedStream{
					Stream: s,
					r:      wstream.R,
				}
				wstreams[i].deferredTorzFiles = wstream.FileListDeferred
			}
			chunks[0] = wstreams
		})
	}
	for i := range upstreams {
		idx := i + chunkIdxOffset
		wg.Go(func() {
			up := &upstreams[i]
			res, err := addon.FetchStream(&stremio_addon.FetchStreamParams{
				BaseURL:  up.baseUrl,
				Type:     rType,
				Id:       id,
				ClientIP: ctx.ClientIP,
			})
			streams := res.Data.Streams
			wstreams := make([]WrappedStream, len(streams))
			errs[idx] = err
			tInfos := []torrent_info.TorrentInfoInsertData{}
			if err == nil {
				extractor, err := up.extractor.Parse()
				if err != nil {
					errs[idx] = err
				} else {
					addonHostname := up.baseUrl.Hostname()
					transformer := StreamTransformer{
						Extractor: extractor,
						Template:  template,
					}
					for i := range streams {
						stream := streams[i]
						if isImdbStremId {
							if cData := torrent_info.ExtractCreateDataFromStream(addonHostname, stremId, &stream); cData != nil {
								tInfos = append(tInfos, *cData)
							}
						}
						wstream, err := transformer.Do(&stream, rType, up.ReconfigureStore)
						if err != nil {
							LogError(r, "failed to transform stream", err)
						}
						if up.NoContentProxy {
							wstream.noContentProxy = true
						}
						wstreams[i] = *wstream
					}
				}
			}
			if isImdbStremId {
				if len(tInfos) > 0 {
					worker.TorrentPusherQueue.Queue(stremId)
				}
				go torrent_info.Upsert(tInfos, torrentInfoCategory, false)
			}
			chunks[idx] = wstreams
		})
	}
	wg.Wait()

	allStreams := []WrappedStream{}
	if ud.IncludeTorz {
		if errs[0] != nil {
			log.Error("failed to fetch torz streams", "error", errs[0])
		} else {
			allStreams = append(allStreams, chunks[0]...)
		}
	}
	for i := range chunks[chunkIdxOffset:] {
		idx := i + chunkIdxOffset
		hostname := upstreams[i].baseUrl.Hostname()
		if errs[idx] != nil {
			log.Error("failed to fetch streams", "error", errs[idx], "hostname", hostname)
		} else {
			allStreams = append(allStreams, chunks[idx]...)
		}
	}

	allStreams, err = filterStoredFileStreams(allStreams, storedFileFilterOptions{
		AllowMixed: config.AllowVideoTorrentsWithDisallowedFiles,
		Mode:       storedFileFilterDisallowedOnly,
	})
	if err != nil {
		return nil, err
	}
	allStreams = filterDisallowedFileStreams(allStreams, config.AllowVideoTorrentsWithDisallowedFiles)

	// Retain torrent candidates before deduplication so a kept direct URL can
	// honor file evidence returned for its matching torrent sibling.
	magnetByHash := map[string]core.MagnetLink{}
	streamCandidates := allStreams
	for i := range allStreams {
		stream := &allStreams[i]
		if (stream.URL == "" || strings.HasPrefix(stream.URL, "magnet:?")) && stream.InfoHash != "" {
			magnet, err := core.ParseMagnetLink(stream.InfoHash)
			if err == nil && magnet.Hash != "" {
				magnetByHash[magnet.Hash] = magnet
			}
		}
	}
	totalStreams := len(allStreams)
	hadDeferredTorzFiles := false
	for _, stream := range allStreams {
		hadDeferredTorzFiles = hadDeferredTorzFiles || stream.deferredTorzFiles
	}
	selectionOptions := streamSelectionOptions{
		Sort:        ud.Sort,
		SortEnabled: template != nil && !hadDeferredTorzFiles,
		IncludeTorz: ud.IncludeTorz,
	}
	allStreams = selectStreams(allStreams, filter, selectionOptions)
	// Only query hashes represented after filtering. Direct-only streams have no
	// torrent candidate and do not trigger an additional provider check.
	hashes := []string{}
	seenHashes := map[string]struct{}{}
	for i := range allStreams {
		hash := wrappedStreamHash(&allStreams[i])
		if _, candidate := magnetByHash[hash]; !candidate {
			continue
		}
		if _, seen := seenHashes[hash]; !seen {
			hashes = append(hashes, hash)
			seenHashes[hash] = struct{}{}
		}
	}

	isCachedByHash := map[string]string{}
	var invalidByHash map[string]struct{}
	var checkedFilesByHash map[string][]store.MagnetFile
	hasErrByStoreCode := map[string]struct{}{}
	var checkMagnetError error
	if len(hashes) > 0 {
		cmRes := ud.CheckMagnet(&store.CheckMagnetParams{
			Magnets:  hashes,
			ClientIP: ctx.ClientIP,
			SId:      stremId,
		}, log)
		if cmRes.HasErr && len(cmRes.ByHash) == 0 {
			checkMagnetError = errors.Join(cmRes.Err...)
		}
		isCachedByHash = cmRes.ByHash
		invalidByHash = cmRes.InvalidByHash
		checkedFilesByHash = cmRes.FilesByHash
		hasErrByStoreCode = cmRes.HasErrByStoreCode
	}

	if checkMagnetError != nil || len(hasErrByStoreCode) > 0 {
		// Partial recheck failures can retain cached hits while leaving other
		// hashes without a store able to provide an uncached playback option.
		hasUncachedStore := false
		if checkMagnetError == nil && !ud.CachedOnly {
			for _, s := range ud.GetStores() {
				if s.Store == nil {
					continue
				}
				code := strings.ToUpper(string(s.Store.GetName().Code()))
				if _, failed := hasErrByStoreCode[code]; !failed && code != "ED" {
					hasUncachedStore = true
					break
				}
			}
		}
		// Remove unavailable replacements before selecting from the original
		// candidates. Cached hits keep their priority over direct URL siblings.
		available := make([]WrappedStream, 0, len(streamCandidates))
		for _, stream := range streamCandidates {
			if (stream.URL == "" || strings.HasPrefix(stream.URL, "magnet:?")) && stream.InfoHash != "" &&
				isCachedByHash[wrappedStreamHash(&stream)] == "" && (!hasUncachedStore || (stream.r != nil && stream.r.IsPrivate)) {
				continue
			}
			available = append(available, stream)
		}
		allStreams = selectStreams(available, filter, selectionOptions)
	}

	if ud.IncludeTorz {
		torzHashes := []string{}
		for _, stream := range allStreams {
			if stream.deferredTorzFiles {
				torzHashes = append(torzHashes, wrappedStreamHash(&stream))
			}
		}
		if len(torzHashes) > 0 {
			checked, err := stremio_torz.GetStreamsForHashesWithCheckedFiles(rType, stremId, torzHashes, nsid, checkedFilesByHash)
			if err != nil {
				return nil, err
			}
			checkedByHash := map[string]stremio_torz.WrappedStream{}
			for _, stream := range checked {
				checkedByHash[stream.R.Hash] = stream
			}
			kept := allStreams[:0]
			for _, stream := range allStreams {
				if stream.deferredTorzFiles {
					fresh, ok := checkedByHash[wrappedStreamHash(&stream)]
					if !ok {
						continue
					}
					tmpl := template
					if tmpl == nil || tmpl.IsEmpty() || tmpl.IsRaw() {
						tmpl = stremio_transformer.StreamTemplateDefault
					}
					rendered, err := tmpl.Execute(fresh.Stream, fresh.R)
					if err != nil {
						return nil, err
					}
					stream.Stream, stream.r = rendered, fresh.R
					stream.deferredTorzFiles = false
				}
				kept = append(kept, stream)
			}
			allStreams = kept
		}
	}
	allStreams, err = filterStoredFileStreams(allStreams, storedFileFilterOptions{
		AllowMixed:   config.AllowVideoTorrentsWithDisallowedFiles,
		CheckedFiles: checkedFilesByHash,
		Mode:         storedFileFilterViability,
	})
	if err != nil {
		return nil, err
	}

	// Resolve pending duplicate groups using final metadata. Other groups already
	// retained their original duplicate priority before the provider check.
	if hadDeferredTorzFiles {
		allStreams = dedupeStreams(filterStreams(allStreams, filter))
		if template != nil {
			stremio_transformer.SortStreams(allStreams, ud.Sort)
		}
	}

	log.Debug("found streams", "total_count", totalStreams, "deduped_count", len(allStreams))

	cachedStreams := []stremio.Stream{}
	uncachedStreams := []stremio.Stream{}
	for i := range allStreams {
		stream := &allStreams[i]
		hash := wrappedStreamHash(stream)
		if _, invalid := invalidByHash[hash]; invalid {
			log.Trace("suppressing torrent with disallowed files or no allowed video files", "hash", hash)
			continue
		}
		if (stream.URL == "" || strings.HasPrefix(stream.URL, "magnet:?")) && stream.InfoHash != "" {
			magnet, ok := magnetByHash[hash]
			if !ok {
				continue
			}
			surl := shared.ExtractRequestBaseURL(r).JoinPath("/stremio/wrap/" + eud + "/_/strem/" + magnet.Hash + "/" + strconv.Itoa(stream.FileIndex) + "/")
			if stream.BehaviorHints != nil && stream.BehaviorHints.Filename != "" {
				surl = surl.JoinPath(url.PathEscape(stream.BehaviorHints.Filename))
			}
			surl.RawQuery = "sid=" + stremId
			if stream.r != nil && stream.r.Season != -1 && stream.r.Episode != -1 {
				surl.RawQuery += "&re=" + url.QueryEscape(strconv.Itoa(stream.r.Season)+".{1,3}"+strconv.Itoa(stream.r.Episode))
			}
			stream.InfoHash = ""
			stream.FileIndex = 0

			storeCode, ok := isCachedByHash[magnet.Hash]
			if ok && storeCode != "" {
				surl.RawQuery += "&s=" + storeCode
				stream.URL = surl.String()
				stream.Name = "⚡ [" + storeCode + "] " + stream.Name

				if ctx.IsProxyAuthorized && config.StoreContentProxy.IsEnabled(string(ud.GetStoreByCode(storeCode).Store.GetName())) {
					stream.Name = "✨ " + stream.Name
				}

				cachedStreams = append(cachedStreams, *stream.Stream)
			} else if !ud.CachedOnly && (stream.r == nil || !stream.r.IsPrivate) {
				surlRawQuery := surl.RawQuery
				stores := ud.GetStores()
				for i := range stores {
					s := &stores[i]
					storeName := s.Store.GetName()
					storeCode := strings.ToUpper(string(storeName.Code()))
					if _, hasErr := hasErrByStoreCode[storeCode]; hasErr || storeCode == "ED" {
						continue
					}

					stream := *stream.Stream
					surl.RawQuery = surlRawQuery + "&s=" + storeCode
					stream.URL = surl.String()
					stream.Name = "[" + storeCode + "] " + stream.Name

					if ctx.IsProxyAuthorized && config.StoreContentProxy.IsEnabled(string(storeName)) && ctx.IsProxyAuthorized {
						stream.Name = "✨ " + stream.Name
					}

					uncachedStreams = append(uncachedStreams, stream)
				}
			}
		} else if stream.URL != "" {
			if !stream.noContentProxy {
				var headers map[string]string
				if stream.BehaviorHints != nil && stream.BehaviorHints.ProxyHeaders != nil && stream.BehaviorHints.ProxyHeaders.Request != nil {
					headers = stream.BehaviorHints.ProxyHeaders.Request
				}

				if ctx.IsProxyAuthorized {
					if url, err := shared.CreateProxyLink(r, stream.URL, headers, config.TUNNEL_TYPE_AUTO, 12*time.Hour, ctx.ProxyAuthUser, ctx.ProxyAuthPassword, true, ""); err == nil && url != stream.URL {
						stream.URL = url
						stream.Name = "✨ " + stream.Name
					}
				}
			}
			if stream.r == nil || stream.r.Store.IsCached {
				cachedStreams = append(cachedStreams, *stream.Stream)
			} else {
				uncachedStreams = append(uncachedStreams, *stream.Stream)
			}
		}
	}

	streams := make([]stremio.Stream, len(cachedStreams)+len(uncachedStreams))
	idx := 0
	for i := range cachedStreams {
		streams[idx] = cachedStreams[i]
		idx++
	}
	for i := range uncachedStreams {
		streams[idx] = uncachedStreams[i]
		idx++
	}

	// Existing direct URLs can survive a failed store lookup. Preserve the error
	// when no usable streams remain, without exposing unconfirmed store options.
	if len(streams) == 0 && checkMagnetError != nil {
		return nil, checkMagnetError
	}
	return &stremio.StreamHandlerResponse{
		Streams: streams,
	}, nil
}

func filterStreams(streams []WrappedStream, filter *stremio_transformer.StreamFilter) []WrappedStream {
	if filter == nil || filter.IsEmpty() {
		return streams
	}
	result := make([]WrappedStream, 0, len(streams))
	for i := range streams {
		stream := &streams[i]
		// A deferred file list must be resolved before metadata-dependent filtering.
		if stream.deferredTorzFiles || stream.r == nil || filter.Match(stream.r) {
			result = append(result, *stream)
		}
	}
	return result
}

func streamHasDisallowedFile(stream *WrappedStream) bool {
	return (stream.BehaviorHints != nil && core.HasDisallowedExtension(stream.BehaviorHints.Filename)) ||
		(stream.r != nil && core.HasDisallowedExtension(stream.r.File.Name))
}

func wrappedStreamHash(stream *WrappedStream) string {
	identifiers := []string{stream.InfoHash}
	if stream.r != nil {
		identifiers = append(identifiers, stream.r.Hash)
	}
	for _, identifier := range identifiers {
		if magnet, err := core.ParseMagnetLink(identifier); err == nil && magnet.Hash != "" {
			return magnet.Hash
		}
	}
	return ""
}

// Retained file evidence applies even when no provider cache status exists.
// Streams without a known hash or stored files pass this filter unchanged.
// Defer no-video rejection until the provider check; strict blocked evidence
// remains decisive before deduplication and provider requests.
type storedFileFilterMode uint8

const (
	storedFileFilterDisallowedOnly storedFileFilterMode = iota
	storedFileFilterViability
)

type storedFileFilterOptions struct {
	AllowMixed   bool
	CheckedFiles map[string][]store.MagnetFile
	Mode         storedFileFilterMode
}

func filterStoredFileStreams(streams []WrappedStream, options storedFileFilterOptions) ([]WrappedStream, error) {
	hashes := util.NewSet[string]()
	for i := range streams {
		if hash := wrappedStreamHash(&streams[i]); hash != "" {
			hashes.Add(hash)
		}
	}
	filesByHash, err := torrent_stream.GetFilesByHashes(hashes.ToSlice())
	if err != nil {
		return nil, err
	}
	result := make([]WrappedStream, 0, len(streams))
	for i := range streams {
		hash := wrappedStreamHash(&streams[i])
		files := filesByHash[hash]
		if (options.Mode == storedFileFilterDisallowedOnly && !options.AllowMixed && files.HasDisallowedFile()) ||
			(options.Mode == storedFileFilterViability && !stremio_shared.StoredFilesAreViable(files, len(options.CheckedFiles[hash]) > 0, options.AllowMixed)) {
			continue
		}
		result = append(result, streams[i])
	}
	return result, nil
}

// Filter before deduplication so a blocked stream cannot hide a usable video.
func filterDisallowedFileStreams(streams []WrappedStream, allowMixed bool) []WrappedStream {
	blockedByHash := map[string]struct{}{}
	if !allowMixed {
		for i := range streams {
			if streamHasDisallowedFile(&streams[i]) {
				if hash := wrappedStreamHash(&streams[i]); hash != "" {
					blockedByHash[hash] = struct{}{}
				}
			}
		}
	}
	result := make([]WrappedStream, 0, len(streams))
	for i := range streams {
		stream := &streams[i]
		if streamHasDisallowedFile(stream) {
			continue
		}
		if !allowMixed {
			if hash := wrappedStreamHash(stream); hash != "" {
				if _, blocked := blockedByHash[hash]; blocked {
					continue
				}
			}
		}
		result = append(result, *stream)
	}
	return result
}

// selectStreams retains Torz's first resolved duplicate before expression filtering
// and sorting. Deferred groups stay intact until file matching resolves them.
// Without Torz, sorting determines the duplicate that is retained.
type streamSelectionOptions struct {
	Sort        string
	SortEnabled bool
	IncludeTorz bool
}

func selectStreams(streams []WrappedStream, filter *stremio_transformer.StreamFilter, options streamSelectionOptions) []WrappedStream {
	if options.IncludeTorz {
		streams = dedupeStreams(streams)
	}
	streams = filterStreams(streams, filter)
	if options.SortEnabled {
		stremio_transformer.SortStreams(streams, options.Sort)
	}
	if !options.IncludeTorz {
		streams = dedupeStreams(streams)
	}
	return streams
}
