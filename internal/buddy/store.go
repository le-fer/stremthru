package buddy

import (
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/magnet_cache"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/worker/worker_queue"
	"github.com/MunifTanjim/stremthru/store"
)

var Buddy = NewAPIClient(&APIClientConfig{
	BaseURL: config.BuddyURL,
})

var buddyLog = logger.Scoped("buddy")

var Peer = peer.NewAPIClient(&peer.APIClientConfig{
	BaseURL: config.PeerURL,
	APIKey:  config.PeerAuthToken,
})

var peerLog = logger.Scoped("buddy:upstream")

func TrackMagnet(s store.Store, hash string, name string, size int64, private bool, files []store.MagnetFile, tInfoCategory torrent_info.TorrentInfoCategory, cacheMiss bool, storeToken string) {
	hash = core.NormalizeMagnetHash(hash)
	if hash == "" {
		return
	}
	storeCode := s.GetName().Code()
	if storeCode.HasUntrustedData() {
		return
	}

	tInfoSource := torrent_info.TorrentInfoSource(storeCode)
	tsFiles := torrent_stream.Files{}
	for _, f := range files {
		source := f.Source
		if source == "" {
			source = string(tInfoSource)
		}
		tsFiles = append(tsFiles, torrent_stream.File{
			Idx:       f.Idx,
			Path:      f.Path,
			Name:      f.Name,
			Size:      f.Size,
			Source:    source,
			VideoHash: f.VideoHash,
			MediaInfo: f.MediaInfo,
		})
	}
	magnet_cache.Touch(s.GetName().Code(), hash, tsFiles, !cacheMiss, true)
	if err := torrent_info.Upsert([]torrent_info.TorrentInfoInsertData{{
		Hash:         hash,
		TorrentTitle: name,
		Size:         size,
		Source:       tInfoSource,
		Files:        tsFiles,
		Private:      private,
	}}, tInfoCategory, storeCode != store.StoreCodeRealDebrid); err != nil {
		buddyLog.Error("failed to track magnet", "store.name", s.GetName(), "hash", hash, "error", core.PackError(err))
	}

	if config.HasBuddy {
		go func() {
			params := &TrackMagnetCacheParams{
				Store:     s.GetName(),
				Hash:      hash,
				Files:     files,
				CacheMiss: cacheMiss,
			}
			start := time.Now()
			if _, err := Buddy.TrackMagnetCache(params); err != nil {
				buddyLog.Error("failed to track magnet cache", "store.name", s.GetName(), "hash", hash, "error", core.PackError(err), "duration", time.Since(start))
			} else {
				buddyLog.Info("track magnet cache", "store.name", s.GetName(), "hash", hash, "duration", time.Since(start))
			}
		}()
	}

	if config.HasPeer && config.PeerAuthToken != "" {
		params := &peer.TrackMagnetParams{
			StoreName:           s.GetName(),
			StoreToken:          storeToken,
			TorrentInfoCategory: tInfoCategory,
			TorrentInfos: []torrent_info.TorrentInfoInsertData{
				{
					Hash:         hash,
					TorrentTitle: name,
					Source:       tInfoSource,
					Size:         size,
					Files:        tsFiles,
					Private:      private,
				},
			},
		}
		go func() {
			start := time.Now()
			if _, err := Peer.TrackMagnet(params); err != nil {
				peerLog.Error("failed to track magnet cache", "store.name", s.GetName(), "hash", hash, "error", core.PackError(err), "duration", time.Since(start))
			} else {
				peerLog.Info("track magnet cache", "store.name", s.GetName(), "hash", hash, "duration", time.Since(start))
			}
		}()
	}
}

type TorrentInfoInput = torrent_info.TorrentInfoInsertData

func BulkTrackMagnet(s store.Store, tInfos []TorrentInfoInput, cached map[string]bool, tInfoCategory torrent_info.TorrentInfoCategory, storeToken string) {
	if len(tInfos) == 0 && len(cached) == 0 {
		return
	}

	storeCode := s.GetName().Code()
	if storeCode.HasUntrustedData() {
		return
	}

	// Canonical keys must agree across file evidence and cache status.
	normalizedCached := make(map[string]bool, len(cached))
	for hash, value := range cached {
		if hash = core.NormalizeMagnetHash(hash); hash != "" {
			normalizedCached[hash] = normalizedCached[hash] || value
		}
	}
	cached = normalizedCached
	normalizedInfos := tInfos[:0]
	for _, info := range tInfos {
		info.Hash = core.NormalizeMagnetHash(info.Hash)
		if info.Hash != "" {
			normalizedInfos = append(normalizedInfos, info)
		}
	}
	tInfos = normalizedInfos
	tInfoSource := torrent_info.TorrentInfoSource(storeCode)
	filesByHash := make(map[string]torrent_stream.Files, len(tInfos))
	for i := range tInfos {
		tInfo := &tInfos[i]
		if tInfo.Source == "" {
			tInfo.Source = tInfoSource
		}
		mergeFilesByHash(filesByHash, tInfo.Hash, tInfo.Files)
	}
	magnet_cache.BulkTouch(s.GetName().Code(), filesByHash, cached, true)
	go torrent_info.Upsert(tInfos, tInfoCategory, storeCode != store.StoreCodeRealDebrid)

	if config.HasBuddy {
		params := &TrackMagnetCacheParams{
			Store:       s.GetName(),
			FilesByHash: filesByHash,
		}
		start := time.Now()
		if _, err := Buddy.TrackMagnetCache(params); err != nil {
			buddyLog.Error("failed to bulk track magnet cache", "error", core.PackError(err), "hash_count", len(tInfos), "store.name", s.GetName(), "duration", time.Since(start))
		} else {
			buddyLog.Info("bulk track magnet cache", "hash_count", len(tInfos), "store.name", s.GetName(), "duration", time.Since(start))
		}
	}

	if config.HasPeer && config.PeerAuthToken != "" {
		params := &peer.TrackMagnetParams{
			StoreName:           s.GetName(),
			StoreToken:          storeToken,
			TorrentInfoCategory: tInfoCategory,
			TorrentInfos:        tInfos,
			Cached:              cached,
		}
		go func() {
			start := time.Now()
			if _, err := Peer.TrackMagnet(params); err != nil {
				peerLog.Error("failed to bulk track magnet cache", "error", core.PackError(err), "hash_count", len(tInfos), "store.name", s.GetName(), "duration", time.Since(start))
			} else {
				peerLog.Info("bulk track magnet cache", "hash_count", len(tInfos), "store.name", s.GetName(), "duration", time.Since(start))
			}
		}()
	}
}

func CheckMagnet(s store.Store, hashes []string, storeToken string, clientIp string, sid string, checkPeer bool) (*store.CheckMagnetData, error) {
	if matched, err := regexp.MatchString("^tt[0-9]+(:[0-9]{1,2}:[0-9]{1,3})?$", sid); err != nil || !matched {
		sid = ""
	}

	data := &store.CheckMagnetData{
		Items: []store.CheckMagnetDataItem{},
	}

	hashes = core.NormalizeMagnetHashes(hashes)
	mcs, err := magnet_cache.GetByHashes(s.GetName().Code(), hashes, sid)
	if err != nil {
		return nil, err
	}
	mcByHash := map[string]magnet_cache.MagnetCache{}
	for _, mc := range mcs {
		mcByHash[core.NormalizeMagnetHash(mc.Hash)] = mc
	}

	magnetByHash := map[string]core.MagnetLink{}

	staleOrMissingHashes := []string{}
	for _, hash := range hashes {
		magnet, err := core.ParseMagnetLink(hash)
		if err != nil {
			continue
		}
		magnetByHash[magnet.Hash] = magnet
		if mc, ok := mcByHash[magnet.Hash]; ok && !mc.IsStale() {
			item := store.CheckMagnetDataItem{
				Hash:   magnet.Hash,
				Magnet: magnet.Link,
				Status: store.MagnetStatusUnknown,
				Files:  mc.Files.ToStoreMagnetFiles(magnet.Hash),
			}
			if mc.IsCached {
				item.Status = store.MagnetStatusCached
			}
			data.Items = append(data.Items, item)
		} else {
			staleOrMissingHashes = append(staleOrMissingHashes, magnet.Hash)
		}
	}

	if len(staleOrMissingHashes) == 0 {
		return data, nil
	}

	if config.HasBuddy {
		params := &CheckMagnetCacheParams{
			Store:    s.GetName(),
			Hashes:   staleOrMissingHashes,
			ClientIP: clientIp,
		}
		params.SId = sid
		start := time.Now()
		res, err := Buddy.CheckMagnetCache(params)
		duration := time.Since(start)
		if err != nil {
			buddyLog.Error("failed to check magnet", "store.name", s.GetName(), "error", core.PackError(err), "duration", duration)
			return data, nil
		} else {
			buddyLog.Info("check magnet", "store.name", s.GetName(), "hash_count", len(staleOrMissingHashes), "duration", duration)
			filesByHash := make(map[string]torrent_stream.Files, len(res.Data.Items))
			evidenceByHash := make(map[string]torrent_stream.Files, len(res.Data.Items))
			cached := make(map[string]bool, len(res.Data.Items))
			for _, item := range res.Data.Items {
				item.Hash = core.NormalizeMagnetHash(item.Hash)
				if item.Hash == "" {
					continue
				}
				res_item := store.CheckMagnetDataItem{
					Hash:   item.Hash,
					Magnet: item.Magnet,
					Status: item.Status,
				}
				res_files := make([]store.MagnetFile, 0, len(item.Files))
				files := make(torrent_stream.Files, 0, len(item.Files))
				// Different supplied names for one path must reach validation,
				// regardless of cache status.
				seenFiles := make(map[[2]string]struct{}, len(item.Files))
				for _, f := range item.Files {
					key := [2]string{f.Path, f.Name}
					if _, seen := seenFiles[key]; seen {
						buddyLog.Info("found duplicate file", "hash", item.Hash, "filename", f.Name)
						continue
					}
					seenFiles[key] = struct{}{}
					source := f.Source
					if source == "" {
						source = string(s.GetName().Code())
					}
					res_files = append(res_files, store.MagnetFile{Idx: f.Idx, Path: f.Path, Name: f.Name, Size: f.Size, Source: source})
					files = append(files, torrent_stream.File{Idx: f.Idx, Path: f.Path, Name: f.Name, Size: f.Size, SId: f.SId, Source: source})
				}
				res_item.Files = res_files
				data.Items = append(data.Items, res_item)
				if item.Status == store.MagnetStatusCached {
					cached[item.Hash] = true
					mergeFilesByHash(filesByHash, item.Hash, files)
				} else if len(files) > 0 {
					mergeFilesByHash(evidenceByHash, item.Hash, files)
				}
			}
			go persistCheckedMagnetEvidence(s.GetName().Code(), filesByHash, cached, evidenceByHash)
			return data, nil
		}
	}

	if checkPeer && config.HasPeer {
		if config.PeerFlag.Lazy {
			storeCode := string(s.GetName().Code())
			for _, hash := range staleOrMissingHashes {
				worker_queue.MagnetCachePullerQueue.Queue(worker_queue.MagnetCachePullerQueueItem{
					ClientIP:   clientIp,
					Hash:       hash,
					SId:        sid,
					StoreCode:  storeCode,
					StoreToken: storeToken,
				})
			}
			return data, nil
		}

		if Peer.IsHaltedCheckMagnet() {
			return data, nil
		}

		var mu sync.Mutex
		var wg sync.WaitGroup
		filesByHash := make(map[string]torrent_stream.Files, len(staleOrMissingHashes))
		evidenceByHash := make(map[string]torrent_stream.Files, len(staleOrMissingHashes))
		for cHashes := range slices.Chunk(staleOrMissingHashes, 500) {
			wg.Go(func() {

				params := &peer.CheckMagnetParams{
					StoreName:  s.GetName(),
					StoreToken: storeToken,
				}
				params.Magnets = cHashes
				params.ClientIP = clientIp
				params.SId = sid
				start := time.Now()
				res, err := Peer.CheckMagnet(params)
				duration := time.Since(start)
				if duration.Seconds() > 10 {
					Peer.HaltCheckMagnet()
				}
				if err != nil {
					peerLog.Error("failed partially to check magnet", "store.name", s.GetName(), "error", core.PackError(err), "duration", duration)
				} else {
					mu.Lock()
					defer mu.Unlock()

					peerLog.Info("check magnet", "store.name", s.GetName(), "hash_count", len(cHashes), "duration", duration)
					if res.Data.RecheckError != nil {
						peerLog.Warn("peer failed to recheck missing file lists; retaining reusable results", "store.name", s.GetName(), "error", res.Data.RecheckError)
						data.RecheckError = errors.Join(data.RecheckError, res.Data.RecheckError)
					}
					for _, item := range res.Data.Items {
						item.Hash = core.NormalizeMagnetHash(item.Hash)
						if item.Hash == "" {
							continue
						}
						files := make(torrent_stream.Files, 0, len(item.Files))
						cleanFiles := make([]store.MagnetFile, 0, len(item.Files))
						// Preserve the same path/name distinctions as Buddy responses.
						seenFiles := make(map[[2]string]struct{}, len(item.Files))
						for _, f := range item.Files {
							key := [2]string{f.Path, f.Name}
							if _, seen := seenFiles[key]; seen {
								peerLog.Info("found duplicate file", "hash", item.Hash, "filename", f.Name)
								continue
							}
							seenFiles[key] = struct{}{}
							if f.Source == "" {
								f.Source = string(s.GetName().Code())
							}
							cleanFiles = append(cleanFiles, f)
							files = append(files, torrent_stream.File{Idx: f.Idx, Path: f.Path, Name: f.Name, Size: f.Size, Source: f.Source, VideoHash: f.VideoHash, MediaInfo: f.MediaInfo})
						}
						item.Files = cleanFiles
						if item.Status == store.MagnetStatusCached {
							mergeFilesByHash(filesByHash, item.Hash, files)
						} else if len(files) > 0 {
							mergeFilesByHash(evidenceByHash, item.Hash, files)
						}
						data.Items = append(data.Items, item)
					}
				}
			})
		}
		wg.Wait()
		go persistCheckedMagnetEvidence(s.GetName().Code(), filesByHash, nil, evidenceByHash)
		return data, nil
	}

	return data, nil
}

func mergeFilesByHash(filesByHash map[string]torrent_stream.Files, hash string, files torrent_stream.Files) {
	if previous, found := filesByHash[hash]; found {
		merged := make(torrent_stream.Files, 0, len(previous)+len(files))
		merged = append(merged, previous...)
		merged = append(merged, files...)
		filesByHash[hash] = merged
	} else {
		filesByHash[hash] = files
	}
}

func persistCheckedMagnetEvidence(storeCode store.StoreCode, filesByHash map[string]torrent_stream.Files, cached map[string]bool, evidenceByHash map[string]torrent_stream.Files) {
	magnet_cache.BulkTouch(storeCode, filesByHash, cached, false)
	if len(evidenceByHash) > 0 {
		torrent_stream.TrackFiles(storeCode, evidenceByHash)
	}
}
