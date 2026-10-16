package stremio_torz

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/anidb"
	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
	"github.com/MunifTanjim/stremthru/internal/shared"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	tznc "github.com/MunifTanjim/stremthru/internal/torznab/client"
	torznab_indexer_syncinfo "github.com/MunifTanjim/stremthru/internal/torznab/indexer/syncinfo"
	"github.com/MunifTanjim/stremthru/internal/torznab/jackett"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/alitto/pond/v2"
)

var streamTemplate = stremio_transformer.StreamTemplateDefault
var torzLazyPull = config.Stremio.Torz.LazyPull

type WrappedStream struct {
	// FileListDeferred marks candidates awaiting a checked cached file list.
	FileListDeferred bool
	*stremio.Stream
	R           *stremio_transformer.StreamExtractorResult
	torrentLink string
}

func (s WrappedStream) IsSortable() bool {
	return s.R != nil
}

func (s WrappedStream) GetQuality() string {
	return s.R.Quality
}

func (s WrappedStream) GetResolution() string {
	return s.R.Resolution
}

func (s WrappedStream) GetSize() string {
	if s.R.File.Size != "" {
		return s.R.File.Size
	}
	return s.R.Size
}

func (s WrappedStream) GetHDR() string {
	return strings.Join(s.R.HDR, "|")
}

type indexerSearchQueryMeta struct {
	titles     []string
	year       int
	season, ep int
}

func (m *indexerSearchQueryMeta) MatchesTitle(parsedTitle string, normalizer *util.StringNormalizer) bool {
	for _, title := range m.titles {
		if util.MaxLevenshteinDistance(5, parsedTitle, title, normalizer) {
			return true
		}
	}
	return false
}

type indexerSearchQuery struct {
	indexer  tznc.Indexer
	query    *tznc.Query
	is_exact bool
}

var torrentFetchPool = pond.NewPool(20)

type indexerStreamsResult struct {
	Streams              []WrappedStream
	InvalidByHash        map[string]struct{}
	DisallowedByHash     map[string]struct{}
	ValidatedVideoByHash map[string]struct{}
}

func getStreamsFromIndexers(reqCtx context.Context, ctx *Ctx, stremType, stremId string) (indexerStreamsResult, error) {
	if len(ctx.Indexers) == 0 {
		return indexerStreamsResult{Streams: []WrappedStream{}}, nil
	}

	log := ctx.Log

	nsid, err := torrent_stream.NormalizeStreamId(stremId)
	if err != nil {
		return indexerStreamsResult{}, err
	}

	queryMeta := indexerSearchQueryMeta{titles: []string{}}
	if nsid.IsAnime {
		if aniEp := util.SafeParseInt(nsid.Episode, -1); aniEp != -1 {
			tvdbMaps, err := anidb.GetTVDBEpisodeMaps(nsid.Id, false)
			if err != nil {
				return indexerStreamsResult{}, err
			}
			if epMap := tvdbMaps.GetByAnidbEpisode(aniEp); epMap != nil {
				ep := epMap.GetTMDBEpisode(aniEp)
				titles, err := anidb.GetTitlesByIds([]string{nsid.Id})
				if err != nil {
					return indexerStreamsResult{}, err
				}
				if len(titles) == 0 {
					return indexerStreamsResult{}, errors.New("no titles found for anidb id: " + nsid.Id)
				}
				queryMeta.titles = make([]string, 0, len(titles))
				queryMeta.season = epMap.TVDBSeason
				queryMeta.ep = ep
				seenTitle := util.NewSet[string]()
				for i := range titles {
					title := &titles[i]
					if seenTitle.Has(title.Value) {
						continue
					}
					seenTitle.Add(title.Value)
					queryMeta.titles = append(queryMeta.titles, title.Value)
					if queryMeta.year == 0 && title.Year != "" {
						queryMeta.year = util.SafeParseInt(title.Year, 0)
					}
				}
			}
		}
	} else {
		it, err := imdb_title.Get(nsid.Id)
		if err != nil {
			return indexerStreamsResult{}, err
		}
		if it == nil {
			return indexerStreamsResult{}, errors.New("imdb title not found: " + nsid.Id)
		}
		queryMeta.titles = append(queryMeta.titles, it.Title)
		if it.OrigTitle != "" && it.OrigTitle != it.Title {
			queryMeta.titles = append(queryMeta.titles, it.OrigTitle)
		}
		if it.Year > 0 {
			queryMeta.year = it.Year
		}
		if nsid.IsSeries() {
			queryMeta.season = util.SafeParseInt(nsid.Season, 0)
			queryMeta.ep = util.SafeParseInt(nsid.Episode, 0)
		}
	}

	sQueries := make([]indexerSearchQuery, 0, len(ctx.Indexers)*2)
	for i := range ctx.Indexers {
		indexer := ctx.Indexers[i]

		query, err := indexer.NewSearchQuery(func(caps tznc.Caps) tznc.Function {
			if nsid.IsSeries() && caps.SupportsFunction(tznc.FunctionSearchTV) {
				return tznc.FunctionSearchTV
			}
			if caps.SupportsFunction(tznc.FunctionSearchMovie) {
				return tznc.FunctionSearchMovie
			}
			return tznc.FunctionSearch
		})
		if err != nil {
			log.Error("failed to create search query", "error", err, "indexer", indexer.GetId())
			continue
		}
		query.SetLimit(-1)
		if !nsid.IsAnime && query.IsSupported(tznc.SearchParamIMDBId) {
			query.Set(tznc.SearchParamIMDBId, nsid.Id)
			is_exact := !nsid.IsSeries()
			if nsid.IsSeries() {
				if query.IsSupported(tznc.SearchParamSeason) && nsid.Season != "" {
					query.Set(tznc.SearchParamSeason, nsid.Season)
					if query.IsSupported(tznc.SearchParamEp) && nsid.Episode != "" {
						query.Set(tznc.SearchParamEp, nsid.Episode)
						is_exact = true
					}
				}
			}
			sQueries = append(sQueries, indexerSearchQuery{
				indexer:  indexer,
				query:    query,
				is_exact: is_exact,
			})
		} else {
			query.SetT(tznc.FunctionSearch)
			supportsYear := query.IsSupported(tznc.SearchParamYear)
			if supportsYear && queryMeta.year != 0 {
				query.Set(tznc.SearchParamYear, strconv.Itoa(queryMeta.year))
			}
			for _, title := range queryMeta.titles {
				var q strings.Builder
				q.WriteString(title)
				if nsid.IsSeries() {
					sQueries = append(sQueries, indexerSearchQuery{
						indexer: indexer,
						query:   query.Clone().Set(tznc.SearchParamQ, q.String()),
					})
					if queryMeta.season > 0 {
						q.WriteString(" S")
						q.WriteString(util.ZeroPadInt(queryMeta.season, 2))
						if queryMeta.ep > 0 {
							sQueries = append(sQueries, indexerSearchQuery{
								indexer: indexer,
								query:   query.Clone().Set(tznc.SearchParamQ, q.String()),
							})
							q.WriteString("E")
							q.WriteString(util.ZeroPadInt(queryMeta.ep, 2))
						}
						sQueries = append(sQueries, indexerSearchQuery{
							indexer: indexer,
							query:   query.Clone().Set(tznc.SearchParamQ, q.String()),
						})
					}
				} else if queryMeta.year > 0 {
					if !supportsYear {
						q.WriteString(" ")
						q.WriteString(strconv.Itoa(queryMeta.year))
					}
					sQueries = append(sQueries, indexerSearchQuery{
						indexer: indexer,
						query:   query.Clone().Set(tznc.SearchParamQ, q.String()),
					})
				}
			}
		}
	}

	var wg sync.WaitGroup
	results := make([][]tznc.Torz, len(sQueries))
	errs := make([]error, len(sQueries))
	for i := range sQueries {
		wg.Add(1)
		go func(sq indexerSearchQuery, i int) {
			defer wg.Done()
			start := time.Now()
			results[i], errs[i] = sq.indexer.Search(reqCtx, sq.query.Values())
			if errs[i] == nil {
				log.Debug("indexer search completed", "indexer", sq.indexer.GetId(), "query", sq.query.Encode(), "duration", time.Since(start).String(), "count", len(results[i]))
			} else {
				log.Error("indexer search failed", "error", errs[i], "indexer", sq.indexer.GetId(), "query", sq.query.Encode(), "duration", time.Since(start).String())
			}
		}(sQueries[i], i)
	}
	wg.Wait()

	realErrs := []error{}
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			realErrs = append(realErrs, err)
		}
	}
	if reqCtx.Err() != nil {
		log.Warn("indexer search timed out, returning partial results")
	} else if len(results) == 0 && len(realErrs) > 0 {
		return indexerStreamsResult{}, errors.Join(realErrs...)
	}

	seenSourceURL := util.NewSet[string]()
	torzFetchWg := torrentFetchPool.NewGroup()
	for _, items := range results {
		for i := range items {
			item := &items[i]
			item.Hash = core.NormalizeMagnetHash(item.Hash)
			if item.HasMissingData() && item.SourceLink != "" {
				if seenSourceURL.Has(item.SourceLink) {
					continue
				}
				seenSourceURL.Add(item.SourceLink)

				torzFetchWg.Submit(func() {
					if reqCtx.Err() != nil {
						return
					}
					err := item.EnsureMagnet(reqCtx)
					if err != nil {
						log.Warn("failed to ensure magnet link for torrent", "error", err)
					}
				})
			}
		}
	}
	err = torzFetchWg.Wait()
	if err != nil {
		log.Warn("errors occurred while fetching torrent magnets", "error", err)
	}

	hashSet := util.NewSet[string]()
	disallowedHashes := util.NewSet[string]()
	allowMixed := config.AllowVideoTorrentsWithDisallowedFiles
	hashes := []string{}
	for _, items := range results {
		for i := range items {
			item := &items[i]
			if item.HasMissingData() {
				continue
			}
			// With mixed torrents disabled, reject a blocked hash across all entries,
			// regardless of their order.
			if !allowMixed && torrent_stream.Files(item.Files).HasDisallowedFile() {
				disallowedHashes.Add(item.Hash)
			}
			if !hashSet.Has(item.Hash) {
				hashSet.Add(item.Hash)
				hashes = append(hashes, item.Hash)
			}
		}
	}

	tInfoByHash, err := torrent_info.GetByHashes(hashes)
	if err != nil {
		return indexerStreamsResult{}, err
	}

	filesByHashes, err := torrent_stream.GetFilesByHashes(hashes)
	if err != nil {
		return indexerStreamsResult{}, err
	}

	if !allowMixed {
		for hash, files := range filesByHashes {
			if files.HasDisallowedFile() {
				disallowedHashes.Add(hash)
			}
		}
	}

	strn := util.NewStringNormalizer()
	tInfosToUpsert := []torrent_info.TorrentItem{}
	rejectedFileEvidence := []torrent_info.TorrentItem{}
	wrappedStreams := []WrappedStream{}
	invalidByHash := map[string]struct{}{}
	validatedVideoByHash := map[string]struct{}{}
	for i, items := range results {
		is_exact := sQueries[i].is_exact
		for i := range items {
			item := &items[i]
			if item.HasMissingData() {
				continue
			}

			files := torrent_stream.Files(item.Files)
			disallowed := disallowedHashes.Has(item.Hash)
			hasAllowedVideo := len(files) > 0 && files.HasAllowedVideo()
			invalid := disallowed || (len(files) > 0 && !hasAllowedVideo)
			if hasAllowedVideo && !disallowed {
				validatedVideoByHash[item.Hash] = struct{}{}
			}
			// Retain usable supplied evidence before excluding an invalid result.
			if invalid && len(item.Files) > 0 {
				evidence := torrent_info.TorrentItem{
					Hash: item.Hash, TorrentTitle: item.Title, Size: item.Size,
					Indexer: item.Indexer, Source: torrent_info.TorrentInfoSourceIndexer,
					Seeders: item.Seeders, Leechers: item.Leechers, Private: item.Private, Files: item.Files,
				}
				rejectedFileEvidence = append(rejectedFileEvidence, evidence)
			}
			if invalid {
				invalidByHash[item.Hash] = struct{}{}
				continue
			}

			if !is_exact {
				pttr, err := util.ParseTorrentTitle(item.Title)
				if err != nil {
					continue
				}
				if !queryMeta.MatchesTitle(pttr.Title, strn) {
					continue
				}
				if nsid.IsSeries() {
					if !slices.Contains(pttr.Seasons, queryMeta.season) {
						continue
					}
					if len(pttr.Episodes) > 0 && !slices.Contains(pttr.Episodes, queryMeta.ep) {
						continue
					}
				} else if queryMeta.year > 0 {
					if pttr.Year != "" && pttr.Year != strconv.Itoa(queryMeta.year) {
						continue
					}
				}
			}

			if tInfo, ok := tInfoByHash[item.Hash]; ok {
				var file *torrent_stream.File
				if files, ok := filesByHashes[item.Hash]; ok {
					idToMatch := stremId
					if idToMatch != "" {
						for i := range files {
							f := &files[i]
							if f.IsAllowedVideo() {
								if f.SId == idToMatch || f.ASId == idToMatch {
									file = f
								}
							}
						}
					}
				}
				fName := ""
				fIdx := -1
				fSize := int64(0)
				fVideoHash := ""
				if file != nil {
					fIdx = file.Idx
					fName = file.Name
					if file.Size > 0 {
						fSize = file.Size
					}
					fVideoHash = file.VideoHash
				} else if core.IsAllowedVideoFile(tInfo.TorrentTitle, "") {
					fName = tInfo.TorrentTitle
				}

				pttr, err := tInfo.ToParsedResult()
				if err != nil {
					return indexerStreamsResult{}, err
				}

				if len(tInfo.TorrentTitle) < len(item.Title)/2 {
					tInfo := torrent_info.TorrentItem{
						Hash:         item.Hash,
						TorrentTitle: item.Title,
						Size:         item.Size,
						Indexer:      item.Indexer,
						Source:       torrent_info.TorrentInfoSourceIndexer,
						Seeders:      item.Seeders,
						Leechers:     item.Leechers,
						Private:      item.Private,
						Files:        item.Files,
					}
					tInfosToUpsert = append(tInfosToUpsert, tInfo)
				}

				data := &stremio_transformer.StreamExtractorResult{
					Hash:      tInfo.Hash,
					TTitle:    tInfo.TorrentTitle,
					Result:    pttr,
					Seeders:   item.Seeders,
					IsPrivate: tInfo.Private || item.Private,
					Indexer: stremio_transformer.StreamExtractorResultIndexer{
						ID:   item.Indexer,
						Name: jackett.GetIndexerName(item.Indexer),
					},
					Kind: stremio_transformer.StreamExtractorResultKindTorz,
					Addon: stremio_transformer.StreamExtractorResultAddon{
						Name: "Torz",
					},
					Category: stremType,
					File: stremio_transformer.StreamExtractorResultFile{
						Name: fName,
						Idx:  fIdx,
					},
				}
				if fSize > 0 {
					data.File.Size = util.ToSize(fSize)
				}
				wrappedStreams = append(wrappedStreams, WrappedStream{
					torrentLink: item.SourceLink,
					R:           data,
					Stream: &stremio.Stream{
						Name:        data.Addon.Name,
						Description: data.TTitle,
						InfoHash:    data.Hash,
						FileIndex:   fIdx,
						BehaviorHints: &stremio.StreamBehaviorHints{
							Filename:   data.File.Name,
							VideoSize:  fSize,
							BingeGroup: "torz:" + data.Hash,
							VideoHash:  fVideoHash,
						},
					},
				})

				continue
			}

			tInfo := torrent_info.TorrentItem{
				Hash:         item.Hash,
				TorrentTitle: item.Title,
				Size:         item.Size,
				Indexer:      item.Indexer,
				Source:       torrent_info.TorrentInfoSourceIndexer,
				Seeders:      item.Seeders,
				Leechers:     item.Leechers,
				Private:      item.Private,
				Files:        item.Files,
			}
			tInfosToUpsert = append(tInfosToUpsert, tInfo)

			pttr, err := util.ParseTorrentTitle(tInfo.TorrentTitle)
			if err != nil {
				return indexerStreamsResult{}, err
			}

			data := &stremio_transformer.StreamExtractorResult{
				Hash:      item.Hash,
				TTitle:    item.Title,
				Seeders:   item.Seeders,
				IsPrivate: item.Private,
				Result:    pttr,
				Indexer: stremio_transformer.StreamExtractorResultIndexer{
					ID:   item.Indexer,
					Name: jackett.GetIndexerName(item.Indexer),
				},
				Kind: stremio_transformer.StreamExtractorResultKindTorz,
				Addon: stremio_transformer.StreamExtractorResultAddon{
					Name: "Torz",
				},
				Category: stremType,
			}

			wrappedStream := WrappedStream{
				torrentLink: item.SourceLink,
				R:           data,
				Stream: &stremio.Stream{
					Name:        data.Addon.Name,
					Description: data.TTitle,
					InfoHash:    data.Hash,
					BehaviorHints: &stremio.StreamBehaviorHints{
						Filename:   data.File.Name,
						BingeGroup: "torz:" + data.Hash,
					},
				},
			}

			if len(item.Files) > 0 {
				var file *torrent_stream.File
				if files, ok := filesByHashes[item.Hash]; ok {
					idToMatch := stremId
					if nsid.IsAnime {
						if nsid.Id == "" {
							idToMatch = ""
						} else {
							idToMatch = nsid.Id + ":" + nsid.Episode
						}
					}
					if idToMatch != "" {
						for i := range files {
							f := &files[i]
							if f.IsAllowedVideo() {
								if f.SId == idToMatch || f.ASId == idToMatch {
									file = f
								}
							}
						}
					}
				}
				if file != nil {
					data.File.Idx = file.Idx
					wrappedStream.Stream.FileIndex = file.Idx
					data.File.Name = file.Name
					if file.Size > 0 {
						data.File.Size = util.ToSize(file.Size)
						wrappedStream.Stream.BehaviorHints.VideoSize = file.Size
					}
					wrappedStream.Stream.BehaviorHints.VideoHash = file.VideoHash
				} else if core.IsAllowedVideoFile(tInfo.TorrentTitle, "") {
					data.File.Name = tInfo.TorrentTitle
				}
			}

			wrappedStreams = append(wrappedStreams, wrappedStream)
		}
	}
	if err := torrent_info.Upsert(rejectedFileEvidence, torrent_info.TorrentInfoCategoryUnknown, false); err != nil {
		log.Warn("failed to retain rejected indexer file evidence", "error", err)
	}
	go torrent_info.Upsert(tInfosToUpsert, torrent_info.TorrentInfoCategoryUnknown, false)

	// Only supplied, validated video evidence clears a non-video-only sibling.
	// A sibling with no file list remains compatible but cannot clear rejection.
	for hash := range validatedVideoByHash {
		delete(invalidByHash, hash)
	}
	disallowedByHash := make(map[string]struct{}, disallowedHashes.Size())
	for _, hash := range disallowedHashes.ToSlice() {
		disallowedByHash[hash] = struct{}{}
	}
	return indexerStreamsResult{
		Streams:              wrappedStreams,
		InvalidByHash:        invalidByHash,
		DisallowedByHash:     disallowedByHash,
		ValidatedVideoByHash: validatedVideoByHash,
	}, nil
}

// Only hashes with a surviving stream candidate need provider availability checks.
func getCheckMagnetHashes(stored, indexed []WrappedStream, disallowed map[string]struct{}) []string {
	hashes := make([]string, 0, len(stored)+len(indexed))
	seen := make(map[string]struct{}, cap(hashes))
	for _, streams := range [][]WrappedStream{stored, indexed} {
		for _, stream := range streams {
			hash := core.NormalizeMagnetHash(stream.R.Hash)
			if hash == "" {
				continue
			}
			if _, blocked := disallowed[hash]; blocked {
				continue
			}
			if _, ok := seen[hash]; ok {
				continue
			}
			seen[hash] = struct{}{}
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

func GetStreamsForHashes(stremType, stremId string, hashes []string, nsid *torrent_stream.NormalizedStremId) ([]WrappedStream, error) {
	return getStreamsForHashes(stremType, stremId, hashes, nsid, storedStreamFileOptions{})
}

// GetStreamCandidatesForHashes defers stored no-video rejection until a store
// check. Callers must validate the candidates with the returned cached file lists.
// A sole opaque video also remains deferred so persistence does not prevent its
// checked-file reconstruction on later requests.
func GetStreamCandidatesForHashes(stremType, stremId string, hashes []string, nsid *torrent_stream.NormalizedStremId) ([]WrappedStream, error) {
	return getStreamsForHashes(stremType, stremId, hashes, nsid, storedStreamFileOptions{DeferMissingVideo: true})
}

// GetStreamsForHashesWithCheckedFiles preserves strict blocked-file and episode
// checks. checkedFiles must contain lists selected and validated by
// UserDataStores.CheckMagnet.
func GetStreamsForHashesWithCheckedFiles(stremType, stremId string, hashes []string, nsid *torrent_stream.NormalizedStremId, checkedFiles map[string][]store.MagnetFile) ([]WrappedStream, error) {
	return getStreamsForHashes(stremType, stremId, hashes, nsid, storedStreamFileOptions{CheckedFiles: checkedFiles})
}

type storedStreamFileOptions struct {
	CheckedFiles      map[string][]store.MagnetFile
	DeferMissingVideo bool
}

func getStreamsForHashes(stremType, stremId string, hashes []string, nsid *torrent_stream.NormalizedStremId, options storedStreamFileOptions) ([]WrappedStream, error) {
	tInfoByHash, err := torrent_info.GetByHashes(hashes)
	if err != nil {
		return nil, err
	}

	filesByHashes, err := torrent_stream.GetFilesByHashes(hashes)
	if err != nil {
		return nil, err
	}

	wrappedStreams := make([]WrappedStream, 0, len(hashes))
	for _, hash := range hashes {
		tInfo, ok := tInfoByHash[hash]
		if !ok {
			continue
		}

		var file *torrent_stream.File
		fileListDeferred := false
		files := filesByHashes[hash]
		if !config.AllowVideoTorrentsWithDisallowedFiles && files.HasDisallowedFile() {
			log.Trace("skipping torrent: disallowed files", "hash", hash, "sid", stremId)
			continue
		}
		// Deferred reconstruction uses the selected checked list for matching,
		// even when storage already contains an allowed but opaque video.
		// Retained strict blocked-file evidence was checked before replacement.
		if checkedFiles := options.CheckedFiles[hash]; len(checkedFiles) > 0 {
			files = make(torrent_stream.Files, 0, len(checkedFiles))
			for _, f := range checkedFiles {
				files = append(files, torrent_stream.File{Name: f.Name, Path: f.Path, Size: f.Size, Idx: f.Idx, Source: f.Source, VideoHash: f.VideoHash, MediaInfo: f.MediaInfo})
			}
			files.Normalize()
			if !stremio_shared.StoredFilesAreViable(files, false, config.AllowVideoTorrentsWithDisallowedFiles) {
				continue
			}
		} else if !files.HasAllowedVideo() {
			if len(files) > 0 {
				if !options.DeferMissingVideo {
					log.Trace("skipping torrent: no video files", "hash", hash, "sid", stremId)
					continue
				}
				fileListDeferred = true
			} else if options.DeferMissingVideo {
				fileListDeferred = true
			}
		}

		if len(files) > 0 {
			idToMatch := stremId
			if nsid.IsAnime {
				if nsid.Id == "" {
					idToMatch = ""
				} else {
					idToMatch = nsid.Id + ":" + nsid.Episode
				}
			}
			if idToMatch != "" {
				for i := range files {
					f := &files[i]
					if f.IsAllowedVideo() {
						if f.SId == idToMatch || f.ASId == idToMatch {
							file = f
							break
						}
					}
				}
			}
			if !fileListDeferred && file == nil && !nsid.IsAnime && nsid.IsSeries() && nsid.Episode != "" {
				videoCount := 0
				opaqueVideo := false
				for i := range files {
					f := &files[i]
					if f.IsAllowedVideo() {
						videoCount++
						data := stremio_shared.ParseSeasonEpisodeFromName(f.Name, false)
						if data.Season != -1 && strconv.Itoa(data.Season) != nsid.Season {
							continue
						}
						if data.Episode == -1 && (f.SId == "" || f.SId == "*") && f.ASId == "" {
							opaqueVideo = true
						}
						if data.Episode == -1 || strconv.Itoa(data.Episode) != nsid.Episode {
							continue
						}
						file = f
						break
					}
				}
				// A newly checked sole video can validate a formerly fileless
				// candidate without identifying an episode. Keep its index unknown
				// and let playback perform existing matching/single-video fallback.
				// Explicit mismatches and ambiguous bundles still fail matching.
				opaqueCandidate := videoCount == 1 && opaqueVideo
				if file == nil && opaqueCandidate && options.DeferMissingVideo {
					fileListDeferred = true
				}
				if file == nil && !fileListDeferred && !(len(options.CheckedFiles[hash]) > 0 && opaqueCandidate) {
					log.Trace("skipping torrent: no matching file found", "hash", tInfo.Hash, "sid", stremId)
					continue
				}
			}
		}

		pttr, err := tInfo.ToParsedResult()
		if err != nil {
			return nil, err
		}
		data := &stremio_transformer.StreamExtractorResult{
			Hash:      tInfo.Hash,
			TTitle:    tInfo.TorrentTitle,
			Result:    pttr,
			Seeders:   tInfo.Seeders,
			IsPrivate: tInfo.Private,
			Indexer: stremio_transformer.StreamExtractorResultIndexer{
				ID:   tInfo.Indexer,
				Name: jackett.GetIndexerName(tInfo.Indexer),
			},
			Addon: stremio_transformer.StreamExtractorResultAddon{
				Name: "Torz",
			},
			Kind:     stremio_transformer.StreamExtractorResultKindTorz,
			Category: stremType,
			File: stremio_transformer.StreamExtractorResultFile{
				Idx: -1,
			},
		}

		fSize := int64(0)
		fVideoHash := ""
		if file != nil {
			data.File.Idx = file.Idx
			data.File.Name = file.Name
			if file.Size > 0 {
				fSize = file.Size
				data.File.Size = util.ToSize(fSize)
			}
			fVideoHash = file.VideoHash

			stremio_transformer.ApplyMediaInfo(data, file.MediaInfo)
		} else if core.IsAllowedVideoFile(tInfo.TorrentTitle, "") {
			data.File.Name = tInfo.TorrentTitle
		}

		wrappedStreams = append(wrappedStreams, WrappedStream{
			R:                data,
			FileListDeferred: fileListDeferred,
			Stream: &stremio.Stream{
				Name:        data.Addon.Name,
				Description: data.TTitle,
				InfoHash:    data.Hash,
				FileIndex:   data.File.Idx,
				BehaviorHints: &stremio.StreamBehaviorHints{
					Filename:   data.File.Name,
					VideoSize:  fSize,
					BingeGroup: "torz:" + data.Hash,
					VideoHash:  fVideoHash,
				},
			},
		})
	}
	return wrappedStreams, nil
}

func handleStream(w http.ResponseWriter, r *http.Request) {
	if !IsMethod(r, http.MethodGet) {
		shared.ErrorMethodNotAllowed(r).Send(w, r)
		return
	}

	ud, err := getUserData(r)
	if err != nil {
		SendError(w, r, err)
		return
	}

	ctx, err := ud.GetRequestContext(r)
	if err != nil {
		shared.ErrorBadRequest(r, "failed to get request context: "+err.Error()).Send(w, r)
		return
	}

	contentType := r.PathValue("contentType")
	id := stremio_shared.GetPathValue(r, "id")

	isImdbId := strings.HasPrefix(id, "tt")
	isKitsuId := strings.HasPrefix(id, "kitsu:")
	isMALId := strings.HasPrefix(id, "mal:")
	isAnime := isKitsuId || isMALId

	if isImdbId {
		if contentType != string(stremio.ContentTypeMovie) && contentType != string(stremio.ContentTypeSeries) {
			shared.ErrorBadRequest(r, "unsupported type: "+contentType).Send(w, r)
			return
		}
	} else if isAnime {
		if contentType != string(stremio.ContentTypeMovie) && contentType != string(stremio.ContentTypeSeries) && contentType != "anime" {
			shared.ErrorBadRequest(r, "unsupported type: "+contentType).Send(w, r)
			return
		}
	} else {
		shared.ErrorBadRequest(r, "unsupported id: "+id).Send(w, r)
		return
	}

	filter, filter_err := ud.GetFilter()
	if filter_err != nil {
		log.Warn("failed to parse filter expression", "error", filter_err)
		shared.ErrorBadRequest(r, "invalid filter expression: "+filter_err.Error()).Send(w, r)
		return
	}

	eud := ud.GetEncoded()

	pulledHashes := []string{}
	nsid, err := torrent_stream.NormalizeStreamId(id)
	if err == nil {
		cleanSId := nsid.ToClean()
		if torzLazyPull {
			go buddy.PullTorrentsByStremId(cleanSId, "")
		} else {
			hashes := buddy.PullTorrentsByStremId(cleanSId, "")
			pulledHashes = append(pulledHashes, hashes...)
		}

		torznab_indexer_syncinfo.QueueJob(nsid.String())
	} else if !errors.Is(err, torrent_stream.ErrUnsupportedStremId) {
		log.Error("failed to normalize strem id", "error", err, "id", id)
		shared.ErrorInternalServerError(r, "failed to normalize strem id").WithCause(err).Send(w, r)
		return
	} else {
		shared.ErrorBadRequest(r, "unsupported strem id: "+id).Send(w, r)
		return
	}

	hashes, err := torrent_info.ListHashesByStremId(id)
	if err != nil {
		SendError(w, r, err)
		return
	}

	hashSet := util.NewSet[string]()
	for _, hash := range hashes {
		hashSet.Add(hash)
	}

	if len(pulledHashes) > 0 {
		for _, hash := range pulledHashes {
			if !hashSet.Has(hash) {
				hashSet.Add(hash)
				hashes = append(hashes, hash)
			}
		}
	}

	// Keep the original lookup set: indexer upserts may finish before this lookup.
	// Newly indexed hashes already have streams and must not be loaded a second time.
	storedHashes := slices.Clone(hashes)
	var storedStreamCandidates []WrappedStream
	var getStoredStreamsError error
	var indexerResult indexerStreamsResult
	var getStreamsFromIndexersError error
	timeoutCtx, cancel := context.WithTimeout(r.Context(), config.Stremio.Torz.IndexerMaxTimeout)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() {
		storedStreamCandidates, getStoredStreamsError = GetStreamCandidatesForHashes(contentType, id, storedHashes, nsid)
	})
	wg.Go(func() {
		indexerResult, getStreamsFromIndexersError = getStreamsFromIndexers(timeoutCtx, ctx, contentType, id)
	})
	wg.Wait()
	if getStoredStreamsError != nil {
		SendError(w, r, getStoredStreamsError)
		return
	}
	log.Debug("fetched streams from indexers", "count", len(indexerResult.Streams))

	if getStreamsFromIndexersError != nil {
		log.Error("failed to get streams from indexers", "error", getStreamsFromIndexersError)
	}

	isP2P := ud.IsP2P()

	var isCachedByHash map[string]string
	var invalidByHash map[string]struct{}
	var checkedFilesByHash map[string][]store.MagnetFile
	var hasErrByStoreCode map[string]struct{}
	var checkMagnetError error
	checkMagnetHashes := getCheckMagnetHashes(storedStreamCandidates, indexerResult.Streams, indexerResult.DisallowedByHash)
	if !isP2P && len(checkMagnetHashes) > 0 {
		cmRes := ud.CheckMagnet(&store.CheckMagnetParams{
			Magnets:  checkMagnetHashes,
			ClientIP: ctx.ClientIP,
			SId:      id,
		}, ctx.Log)
		if cmRes.HasErr && len(cmRes.ByHash) == 0 {
			checkMagnetError = errors.Join(cmRes.Err...)
		} else {
			isCachedByHash = cmRes.ByHash
			invalidByHash = cmRes.InvalidByHash
			checkedFilesByHash = cmRes.FilesByHash
			hasErrByStoreCode = cmRes.HasErrByStoreCode
		}
	}

	if checkMagnetError != nil {
		SendError(w, r, checkMagnetError)
		return
	}

	wrappedStreams := storedStreamCandidates
	deferredHashes := []string{}
	seenDeferredHashes := map[string]struct{}{}
	for _, stream := range wrappedStreams {
		if stream.FileListDeferred {
			if _, seen := seenDeferredHashes[stream.R.Hash]; !seen {
				deferredHashes = append(deferredHashes, stream.R.Hash)
				seenDeferredHashes[stream.R.Hash] = struct{}{}
			}
		}
	}
	if len(deferredHashes) > 0 {
		resolved, resolveErr := GetStreamsForHashesWithCheckedFiles(contentType, id, deferredHashes, nsid, checkedFilesByHash)
		if resolveErr != nil {
			SendError(w, r, resolveErr)
			return
		}
		resolvedByHash := make(map[string]WrappedStream, len(resolved))
		for _, stream := range resolved {
			resolvedByHash[stream.R.Hash] = stream
		}
		kept := wrappedStreams[:0]
		for _, stream := range wrappedStreams {
			if !stream.FileListDeferred {
				kept = append(kept, stream)
			} else if fresh, ok := resolvedByHash[stream.R.Hash]; ok {
				kept = append(kept, fresh)
			}
		}
		wrappedStreams = kept
	}
	if getStreamsFromIndexersError == nil {
		wrappedStreams = mergeIndexerStreams(wrappedStreams, indexerResult, checkedFilesByHash)
	}
	// Load evidence only for candidates that survived merging. The original hash
	// set can be much larger and cannot affect the response once it has no stream.
	viabilityHashes := getCheckMagnetHashes(wrappedStreams, nil, nil)
	storedFiles, err := torrent_stream.GetFilesByHashes(viabilityHashes)
	if err != nil {
		SendError(w, r, err)
		return
	}
	viableStreams := wrappedStreams[:0]
	for _, stream := range wrappedStreams {
		_, validatedByIndexer := indexerResult.ValidatedVideoByHash[stream.R.Hash]
		if stremio_shared.StoredFilesAreViable(storedFiles[stream.R.Hash], validatedByIndexer || len(checkedFilesByHash[stream.R.Hash]) > 0, config.AllowVideoTorrentsWithDisallowedFiles) {
			viableStreams = append(viableStreams, stream)
		}
	}
	wrappedStreams = filterStreams(viableStreams, filter)

	stremio_transformer.SortStreams(wrappedStreams, ud.Sort)

	streamBaseUrl := ExtractRequestBaseURL(r).JoinPath("/stremio/torz", eud, "_/strem", id)

	cachedStreams := []stremio.Stream{}
	uncachedStreams := []stremio.Stream{}
	for _, wStream := range wrappedStreams {
		hash := wStream.R.Hash

		if _, invalid := invalidByHash[hash]; invalid {
			log.Trace("suppressing torrent with disallowed files or no allowed video files", "hash", hash, "sid", id)
			continue
		}

		if isP2P {
			if wStream.FileIndex == -1 || wStream.R.IsPrivate {
				continue
			}

			wStream.R.Store.Code = "P2P"
			wStream.R.Store.Name = "P2P"
			stream, err := streamTemplate.Execute(wStream.Stream, wStream.R)
			if err != nil {
				SendError(w, r, err)
				return
			}
			uncachedStreams = append(uncachedStreams, *stream)
		} else if storeCode, isCached := isCachedByHash[hash]; isCached && storeCode != "" {
			storeName := store.StoreCode(strings.ToLower(storeCode)).Name()
			wStream.R.Store.Code = storeCode
			wStream.R.Store.Name = string(storeName)
			wStream.R.Store.IsCached = true
			wStream.R.Store.IsProxied = ctx.IsProxyAuthorized && config.StoreContentProxy.IsEnabled(string(storeName))
			stream, err := streamTemplate.Execute(wStream.Stream, wStream.R)
			if err != nil {
				SendError(w, r, err)
				return
			}
			identifier := hash
			steamUrl := streamBaseUrl.JoinPath(strings.ToLower(storeCode), identifier, strconv.Itoa(wStream.R.File.Idx), "/")
			if wStream.R.File.Name != "" {
				steamUrl = steamUrl.JoinPath(url.PathEscape(wStream.R.File.Name))
			}
			stream.URL = steamUrl.String()
			stream.InfoHash = ""
			stream.FileIndex = 0
			cachedStreams = append(cachedStreams, *stream)
		} else if !ud.CachedOnly {
			stores := ud.GetStores()
			for i := range stores {
				s := &stores[i]
				storeName := s.Store.GetName()
				storeCode := storeName.Code()
				if _, hasErr := hasErrByStoreCode[strings.ToUpper(string(storeCode))]; hasErr || storeCode == store.StoreCodeEasyDebrid {
					continue
				}
				if wStream.R.IsPrivate && (!ud.IncludeUncachedPrivate || wStream.torrentLink == "" || storeCode != store.StoreCodeTorBox) {
					continue
				}

				origStream := *wStream.Stream
				wStream.R.Store.Code = strings.ToUpper(string(storeCode))
				wStream.R.Store.Name = string(storeName)
				wStream.R.Store.IsProxied = ctx.IsProxyAuthorized && config.StoreContentProxy.IsEnabled(string(storeName))
				stream, err := streamTemplate.Execute(&origStream, wStream.R)
				if err != nil {
					SendError(w, r, err)
					return
				}

				identifier := hash
				if wStream.R.IsPrivate {
					identifier += "-" + util.Base64Encode(wStream.torrentLink)
				}
				steamUrl := streamBaseUrl.JoinPath(string(storeCode), identifier, strconv.Itoa(wStream.R.File.Idx), "/")
				if wStream.R.File.Name != "" {
					steamUrl = steamUrl.JoinPath(url.PathEscape(wStream.R.File.Name))
				}
				stream.URL = steamUrl.String()
				stream.InfoHash = ""
				stream.FileIndex = 0
				uncachedStreams = append(uncachedStreams, *stream)
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

	if isP2P && !torzLazyPull {
		w.Header().Set("Cache-Control", "public, max-age=7200")
	}

	SendResponse(w, r, 200, &stremio.StreamHandlerResponse{
		Streams: streams,
	})
}

func filterStreams(streams []WrappedStream, filter *stremio_transformer.StreamFilter) []WrappedStream {
	if filter == nil || filter.IsEmpty() {
		return streams
	}
	result := make([]WrappedStream, 0, len(streams))
	for i := range streams {
		stream := &streams[i]
		if stream.R == nil || filter.Match(stream.R) {
			result = append(result, *stream)
		}
	}
	return result
}

// mergeIndexerStreams applies newly discovered rejection evidence to both sources.
// Rejection evidence excludes matching hashes from both stored and indexed streams.
func mergeIndexerStreams(stored []WrappedStream, indexed indexerStreamsResult, checkedFilesByHash map[string][]store.MagnetFile) []WrappedStream {
	streams := make([]WrappedStream, 0, len(stored)+len(indexed.Streams))
	for _, source := range [][]WrappedStream{stored, indexed.Streams} {
		for _, stream := range source {
			if _, invalid := indexed.InvalidByHash[stream.R.Hash]; invalid {
				_, disallowed := indexed.DisallowedByHash[stream.R.Hash]
				if disallowed || len(checkedFilesByHash[stream.R.Hash]) == 0 {
					continue
				}
			}
			streams = append(streams, stream)
		}
	}
	return streams
}
