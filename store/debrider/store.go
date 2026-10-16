package debrider

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/buddy"
	"github.com/MunifTanjim/stremthru/internal/cache"
	"github.com/MunifTanjim/stremthru/internal/config"
	store_util "github.com/MunifTanjim/stremthru/internal/store/util"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/MunifTanjim/stremthru/store/stats"
)

func getMagnetStatusFromTaskStatus(status TaskStatus) store.MagnetStatus {
	switch status {
	case TaskStatusError:
		return store.MagnetStatusFailed
	case TaskStatusParsing:
		return store.MagnetStatusQueued
	case TaskStatusDownloading:
		return store.MagnetStatusDownloading
	case TaskStatusCompleted:
		return store.MagnetStatusDownloaded
	default:
		return store.MagnetStatusUnknown
	}
}

type StoreClientConfig struct {
	HTTPClient *http.Client
	UserAgent  string
}

type StoreClient struct {
	Name   store.StoreName
	client *APIClient
	config *StoreClientConfig

	generateLinkCache cache.Cache[store.GenerateLinkData]
}

func (s *StoreClient) GetName() store.StoreName {
	return s.Name
}

type LockedFileLink string

const lockedFileLinkPrefix = "stremthru://store/debrider/"

func (l LockedFileLink) encodeData(taskId, fileName string) string {
	return util.Base64Encode(taskId + ":" + fileName)
}

func (l LockedFileLink) decodeData(encoded string) (taskId, fileName string, err error) {
	decoded, err := util.Base64Decode(encoded)
	if err != nil {
		return "", "", err
	}
	tId, fName, found := strings.Cut(decoded, ":")
	if !found {
		return "", "", err
	}
	return tId, fName, nil
}

func (l LockedFileLink) Create(taskId, fileName string) string {
	return lockedFileLinkPrefix + l.encodeData(taskId, fileName)
}

func (l LockedFileLink) Parse() (taskId, fileName string, err error) {
	encoded := strings.TrimPrefix(string(l), lockedFileLinkPrefix)
	return l.decodeData(encoded)
}

func (s *StoreClient) AddMagnet(params *store.AddMagnetParams) (*store.AddMagnetData, error) {
	var magnet *core.MagnetLink
	var isPrivate bool
	if params.Magnet != "" {
		m, err := core.ParseMagnetLink(params.Magnet)
		if err != nil {
			return nil, err
		}
		magnet = &m
	} else {
		mi, mii, err := params.GetTorrentMeta()
		if err != nil {
			return nil, err
		}
		isPrivate = util.PtrToBool(mii.Private, false)
		m, err := core.ParseMagnetLink(mi.HashInfoBytes().HexString())
		if err != nil {
			return nil, err
		}
		magnet = &m
	}

	cdt_params := &CreateDownloadTaskParams{
		Ctx: params.Ctx,
	}
	if params.Magnet != "" {
		cdt_params.Type = DownloadTaskTypeMagnet
		cdt_params.Data = CreateDownloadTaskParamsData{
			MagnetLink: magnet.RawLink,
		}
	} else {
		cdt_params.Type = DownloadTaskTypeTorrent
		cdt_params.Data = CreateDownloadTaskParamsData{
			FileContent: params.Torrent,
		}
	}
	start := time.Now()
	res, err := s.client.CreateDownloadTask(cdt_params)
	stats.Record(s.Name, "add_torz", time.Since(start), err != nil)
	if err != nil {
		return nil, err
	}
	data := &store.AddMagnetData{
		Id:      res.Data.Id,
		Hash:    magnet.Hash,
		Magnet:  magnet.Link,
		Name:    res.Data.Name,
		Size:    res.Data.Size,
		Status:  getMagnetStatusFromTaskStatus(res.Data.Status),
		Private: isPrivate,
		Files:   []store.MagnetFile{},
		AddedAt: res.Data.GetAddedAt(),
	}
	source := string(s.GetName().Code())
	for i := range res.Data.Files {
		f := &res.Data.Files[i]
		data.Files = append(data.Files, store.MagnetFile{
			Idx:    i,
			Link:   LockedFileLink("").Create(res.Data.Id, f.Name),
			Name:   f.GetName(),
			Path:   f.GetPath(),
			Size:   f.Size,
			Source: source,
		})
	}
	return data, nil

}

// mapLinkAvailabilityResults prefers explicit identities over positions. Hashless
// entries retain upstream's positional handling only for a complete response
// whose explicit identities agree with that ordering. Sparse identified results
// are usable; absent results say nothing about the remaining hashes.
func mapLinkAvailabilityResults(hashes []string, results []CheckLinkAvailabilityDataItem) (map[string]CheckLinkAvailabilityDataItem, error) {
	requested := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		requested[hash] = struct{}{}
	}
	positional := len(results) == len(hashes)
	hashless := len(results) == 0
	for i, detail := range results {
		if detail.Hash == "" {
			hashless = true
			continue
		}
		hash := core.NormalizeMagnetHash(detail.Hash)
		if _, found := requested[hash]; hash == "" || !found {
			return nil, fmt.Errorf("invalid link availability response: invalid or unrequested hash")
		}
		if i >= len(hashes) || hash != hashes[i] {
			positional = false
		}
	}
	if hashless && !positional {
		return nil, fmt.Errorf("invalid link availability response: ambiguous identities (%d results for %d magnets)", len(results), len(hashes))
	}
	byHash := make(map[string]CheckLinkAvailabilityDataItem, len(results))
	for i, detail := range results {
		hash := core.NormalizeMagnetHash(detail.Hash)
		if detail.Hash == "" {
			hash = hashes[i]
		}
		if previous, found := byHash[hash]; found {
			if previous.Cached != detail.Cached {
				return nil, fmt.Errorf("invalid link availability response: conflicting cached statuses")
			}
			previous.Files = append(previous.Files, detail.Files...)
			byHash[hash] = previous
		} else {
			detail.Hash = hash
			detail.Files = slices.Clone(detail.Files)
			byHash[hash] = detail
		}
	}
	return byHash, nil
}

func (s *StoreClient) CheckMagnet(params *store.CheckMagnetParams) (*store.CheckMagnetData, error) {
	totalMagnets := len(params.Magnets)

	magnetByHash := make(map[string]core.MagnetLink, totalMagnets)
	hashes := make([]string, totalMagnets)

	for i, m := range params.Magnets {
		magnet, err := core.ParseMagnetLink(m)
		if err != nil {
			return nil, err
		}
		magnetByHash[magnet.Hash] = magnet
		hashes[i] = magnet.Hash
	}

	var partition store_util.CheckMagnetItemsPartition
	if data, err := buddy.CheckMagnet(s, hashes, params.GetAPIKey(s.client.apiKey), params.ClientIP, params.SId, true); err != nil {
		return nil, err
	} else {
		partition = store_util.PartitionCheckMagnetItems(store_util.PartitionCheckMagnetItemsParams{
			Items:                   data.Items,
			RecheckError:            data.RecheckError,
			RecheckMissingFileLists: config.StoreRecheckMissingFileLists,
			LocalOnly:               params.LocalOnly,
		})
	}

	if params.LocalOnly {
		data := &store.CheckMagnetData{
			RecheckError: partition.RecheckError,
			Items:        []store.CheckMagnetDataItem{},
		}

		for _, hash := range hashes {
			if item, ok := partition.Found[hash]; ok {
				data.Items = append(data.Items, item)
			}
		}
		return data, nil
	}

	missingHashes := []string{}
	missingLinks := []string{}
	for _, hash := range hashes {
		if _, ok := partition.Found[hash]; !ok {
			magnet := magnetByHash[hash]
			missingHashes = append(missingHashes, magnet.Hash)
			missingLinks = append(missingLinks, magnet.Link)
		}
	}

	ldByHash := map[string]CheckLinkAvailabilityDataItem{}
	if len(missingHashes) > 0 {
		start := time.Now()
		res, err := s.client.CheckLinkAvailability(&CheckLinkAvailabilityParams{
			Ctx:  params.Ctx,
			Data: missingLinks,
		})
		if err == nil {
			ldByHash, err = mapLinkAvailabilityResults(missingHashes, res.Data.Result)
		}
		info := stats.CheckCall(partition.FileListRecheckCount(missingHashes, params.FileListRecheckHashes), len(missingHashes))
		stats.RecordWithInfo(s.Name, "check_torz", time.Since(start), err != nil, info)
		if err != nil {
			return partition.Restore(hashes, err)
		}
	}
	data := &store.CheckMagnetData{
		RecheckError: partition.RecheckError,
		Items:        []store.CheckMagnetDataItem{},
	}
	tInfos := []buddy.TorrentInfoInput{}
	source := string(s.GetName().Code())
	for _, hash := range hashes {
		if item, ok := partition.Found[hash]; ok {
			data.Items = append(data.Items, item)
			continue
		}
		if _, resolved := ldByHash[hash]; !resolved {
			if item, reusable := partition.Reusable[hash]; reusable {
				data.Items = append(data.Items, item)
				continue
			}
		}

		magnet := magnetByHash[hash]
		item := store.CheckMagnetDataItem{
			Hash:   magnet.Hash,
			Magnet: magnet.Link,
			Status: store.MagnetStatusUnknown,
			Files:  []store.MagnetFile{},
		}
		tInfo := buddy.TorrentInfoInput{
			Hash:  hash,
			Files: []torrent_stream.File{},
		}
		if detail, ok := ldByHash[hash]; ok {
			if detail.Cached {
				item.Status = store.MagnetStatusCached
				for idx, f := range detail.Files {
					file := torrent_stream.File{
						Idx:    idx,
						Path:   f.GetPath(),
						Name:   f.GetName(),
						Size:   f.Size,
						Source: source,
					}
					tInfo.Files = append(tInfo.Files, file)
					item.Files = append(item.Files, store.MagnetFile{
						Idx:    file.Idx,
						Path:   file.Path,
						Name:   file.Name,
						Size:   file.Size,
						Source: file.Source,
					})
				}
			}
		}
		data.Items = append(data.Items, item)
		tInfos = append(tInfos, tInfo)
	}
	go buddy.BulkTrackMagnet(s, tInfos, nil, "", params.GetAPIKey(s.client.apiKey))
	return data, nil
}

func (s *StoreClient) getCachedGeneratedLink(params *store.GenerateLinkParams, taskId, fileName string) *store.GenerateLinkData {
	v := &store.GenerateLinkData{}
	if s.generateLinkCache.Get(params.GetAPIKey(s.client.apiKey)+":"+taskId+":"+fileName, v) {
		return v
	}
	return nil

}

func (s *StoreClient) setCachedGenerateLink(params *store.GenerateLinkParams, taskId, fileName string, v *store.GenerateLinkData) {
	s.generateLinkCache.Add(params.GetAPIKey(s.client.apiKey)+":"+taskId+":"+fileName, *v)
}

func (s *StoreClient) GenerateLink(params *store.GenerateLinkParams) (*store.GenerateLinkData, error) {
	taskId, fileName, err := LockedFileLink(params.Link).Parse()
	if err != nil {
		error := core.NewAPIError("invalid link")
		error.StoreName = string(store.StoreNameDebrider)
		error.StatusCode = http.StatusBadRequest
		error.Cause = err
		return nil, error
	}
	if v := s.getCachedGeneratedLink(params, taskId, fileName); v != nil {
		return v, nil
	}
	start := time.Now()
	res, err := s.client.GetTask(&GetTaskParams{
		Ctx: params.Ctx,
		Id:  taskId,
	})
	stats.Record(s.Name, "generate_torz_link", time.Since(start), err != nil)
	if err != nil {
		return nil, err
	}
	link := ""
	for i := range res.Data.Files {
		f := &res.Data.Files[i]
		if f.Name == fileName {
			link = f.DownloadLink
		}
	}
	if link == "" {
		err := core.NewAPIError("file not found")
		err.StoreName = string(store.StoreNameDebrider)
		err.StatusCode = http.StatusNotFound
		return nil, err
	}
	data := &store.GenerateLinkData{Link: link}
	s.setCachedGenerateLink(params, taskId, fileName, data)
	return data, nil
}

func (s *StoreClient) GetMagnet(params *store.GetMagnetParams) (*store.GetMagnetData, error) {
	start := time.Now()
	res, err := s.client.GetTask(&GetTaskParams{
		Ctx: params.Ctx,
		Id:  params.Id,
	})
	stats.Record(s.Name, "get_torz", time.Since(start), err != nil)
	if err != nil {
		return nil, err
	}
	if res.Data.Type != "torrent" {
		err := core.NewAPIError("not found")
		err.StatusCode = 404
		err.StoreName = string(store.StoreNameDebrider)
		return nil, err
	}
	data := &store.GetMagnetData{
		Id:      res.Data.Id,
		Hash:    res.Data.Hash,
		Name:    res.Data.Name,
		Size:    res.Data.Size,
		Status:  getMagnetStatusFromTaskStatus(res.Data.Status),
		Files:   []store.MagnetFile{},
		AddedAt: res.Data.GetAddedAt(),
	}
	source := string(s.GetName().Code())
	for i := range res.Data.Files {
		f := &res.Data.Files[i]
		data.Files = append(data.Files, store.MagnetFile{
			Idx:    i,
			Link:   LockedFileLink("").Create(res.Data.Id, f.Name),
			Name:   f.GetName(),
			Path:   f.GetPath(),
			Size:   f.Size,
			Source: source,
		})
	}
	return data, nil
}

func (s *StoreClient) GetUser(params *store.GetUserParams) (*store.User, error) {
	start := time.Now()
	res, err := s.client.GetAccount(&GetAccountParams{
		Ctx: params.Ctx,
	})
	stats.Record(s.Name, "get_user", time.Since(start), err != nil)
	if err != nil {
		return nil, err
	}
	data := &store.User{
		Id:                 res.Data.Id,
		Email:              res.Data.Email,
		SubscriptionStatus: store.UserSubscriptionStatusExpired,
	}
	switch res.Data.Subscription.Status {
	case "active":
		data.SubscriptionStatus = store.UserSubscriptionStatusPremium
	case "trialing":
		data.SubscriptionStatus = store.UserSubscriptionStatusTrial
	case "canceled":
		data.SubscriptionStatus = store.UserSubscriptionStatusExpired
	}
	return data, err
}

func (s *StoreClient) ListMagnets(params *store.ListMagnetsParams) (*store.ListMagnetsData, error) {
	start := time.Now()
	res, err := s.client.ListTask(&ListTaskParams{
		Ctx: params.Ctx,
	})
	stats.Record(s.Name, "list_torz", time.Since(start), err != nil)
	if err != nil {
		return nil, err
	}

	items := []store.ListMagnetsDataItem{}
	for i := range res.Data {
		task := &res.Data[i]
		if task.Type != "torrent" {
			continue
		}
		item := store.ListMagnetsDataItem{
			Id:      task.Id,
			Hash:    task.Hash,
			Name:    task.Name,
			Size:    task.Size,
			Status:  getMagnetStatusFromTaskStatus(task.Status),
			AddedAt: task.GetAddedAt(),
		}
		items = append(items, item)
	}

	data := &store.ListMagnetsData{
		Items:      items,
		TotalItems: len(items),
	}
	return data, nil
}

func (s *StoreClient) RemoveMagnet(params *store.RemoveMagnetParams) (*store.RemoveMagnetData, error) {
	start := time.Now()
	_, err := s.client.DeleteTask(&DeleteTaskParams{
		Ctx: params.Ctx,
		Id:  params.Id,
	})
	stats.Record(s.Name, "remove_torz", time.Since(start), err != nil)
	if err != nil {
		return nil, err
	}
	data := &store.RemoveMagnetData{
		Id: params.Id,
	}
	return data, nil
}

func NewStoreClient(config *StoreClientConfig) *StoreClient {
	c := &StoreClient{}
	c.client = NewAPIClient(&APIClientConfig{
		HTTPClient: config.HTTPClient,
		UserAgent:  config.UserAgent,
	})
	c.Name = store.StoreNameDebrider
	c.config = config

	c.generateLinkCache = func() cache.Cache[store.GenerateLinkData] {
		return cache.NewCache[store.GenerateLinkData](&cache.CacheConfig{
			Name:     "store:debrider:generateLink",
			Lifetime: 30 * time.Minute,
		})
	}()

	return c
}
