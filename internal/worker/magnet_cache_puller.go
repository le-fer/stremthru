package worker

import (
	"slices"
	"strings"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/magnet_cache"
	"github.com/MunifTanjim/stremthru/internal/peer"
	"github.com/MunifTanjim/stremthru/internal/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/worker/worker_queue"
	"github.com/MunifTanjim/stremthru/store"
)

func InitMagnetCachePullerWorker(conf *WorkerConfig) *Worker {
	conf.Executor = func(w *Worker) error {
		worker_queue.MagnetCachePullerQueue.ProcessGroup(func(key string, items []worker_queue.MagnetCachePullerQueueItem) error {
			storeCode, sid, _ := strings.Cut(key, ":")

			s := shared.GetStoreByCode(storeCode)
			if s == nil {
				w.Log.Error("invalid store code", "store.code", storeCode)
				return nil
			}

			if store.StoreCode(storeCode).HasUntrustedData() {
				return nil
			}

			hashes := make([]string, len(items))
			clientIps := []string{}
			seenClientIp := map[string]struct{}{}
			storeTokens := []string{}
			seenStoreToken := map[string]struct{}{}
			for i := range items {
				item := &items[i]
				hashes[i] = item.Hash
				if _, seen := seenClientIp[item.ClientIP]; !seen {
					clientIps = append(clientIps, item.ClientIP)
					seenClientIp[item.ClientIP] = struct{}{}
				}
				if _, seen := seenStoreToken[item.StoreToken]; !seen {
					storeTokens = append(storeTokens, item.StoreToken)
					seenStoreToken[item.StoreToken] = struct{}{}
				}
			}

			hashes = core.NormalizeMagnetHashes(hashes)
			for i, cHashes := range slices.Collect(slices.Chunk(hashes, 500)) {
				if buddy.Peer.IsHaltedCheckMagnet() {
					time.Sleep(15 * time.Second)
				}

				storeToken := storeTokens[i%len(storeTokens)]
				clientIp := clientIps[i%len(clientIps)]

				params := &peer.CheckMagnetParams{
					StoreName:  s.GetName(),
					StoreToken: storeToken,
				}
				params.Magnets = cHashes
				params.ClientIP = clientIp
				params.SId = sid
				start := time.Now()
				res, err := buddy.Peer.CheckMagnet(params)
				duration := time.Since(start)
				if duration.Seconds() > 10 {
					Peer.HaltCheckMagnet()
				}
				if err != nil {
					w.Log.Error("failed partially to check magnet", "store.name", s.GetName(), "error", core.PackError(err), "duration", duration)
				} else {
					w.Log.Info("check magnet", "store.name", s.GetName(), "hash_count", len(cHashes), "duration", duration)
					recordMagnetCachePullResult(s.GetName().Code(), res.Data.Items, w.Log)
				}
			}

			return nil
		})

		return nil
	}

	worker := NewWorker(conf)

	return worker
}

func recordMagnetCachePullResult(storeCode store.StoreCode, items []store.CheckMagnetDataItem, log *logger.Logger) {
	filesByHash := make(map[string]torrent_stream.Files, len(items))
	evidenceByHash := make(map[string]torrent_stream.Files, len(items))
	cached := make(map[string]bool, len(items))
	for _, item := range items {
		hash := core.NormalizeMagnetHash(item.Hash)
		if hash == "" {
			continue
		}
		files := collectMagnetCacheFiles(hash, item.Files, log)
		if item.Status == store.MagnetStatusCached {
			cached[hash] = true
			mergeMagnetCacheFiles(filesByHash, hash, files)
		} else if len(files) > 0 {
			// BulkTouch treats nonempty files as cached, so retain miss evidence separately.
			mergeMagnetCacheFiles(evidenceByHash, hash, files)
		}
	}

	magnet_cache.BulkTouch(storeCode, filesByHash, cached, false)
	if len(evidenceByHash) > 0 {
		torrent_stream.TrackFiles(storeCode, evidenceByHash)
	}
}

func mergeMagnetCacheFiles(filesByHash map[string]torrent_stream.Files, hash string, files torrent_stream.Files) {
	if previous, found := filesByHash[hash]; found {
		merged := make(torrent_stream.Files, 0, len(previous)+len(files))
		merged = append(merged, previous...)
		merged = append(merged, files...)
		filesByHash[hash] = merged
	} else {
		filesByHash[hash] = files
	}
}

// Preserve distinct paths and names so deduplication cannot hide blocked evidence.
func collectMagnetCacheFiles(hash string, input []store.MagnetFile, log *logger.Logger) torrent_stream.Files {
	files := make(torrent_stream.Files, 0, len(input))
	seenFiles := make(map[[2]string]struct{}, len(input))
	for _, f := range input {
		key := [2]string{f.Path, f.Name}
		if _, seen := seenFiles[key]; seen {
			log.Info("found duplicate file", "hash", hash, "filename", f.Name)
			continue
		}
		seenFiles[key] = struct{}{}
		files = append(files, torrent_stream.File{
			Idx:       f.Idx,
			Path:      f.Path,
			Name:      f.Name,
			Size:      f.Size,
			Source:    f.Source,
			VideoHash: f.VideoHash,
			MediaInfo: f.MediaInfo,
		})
	}
	return files
}
