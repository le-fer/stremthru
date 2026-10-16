package stremio_userdata

import (
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/shared"
	store_util "github.com/MunifTanjim/stremthru/internal/store/util"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
)

var IsPublicInstance = config.IsPublicInstance

type StoreCode string

func (sc StoreCode) IsStremThru() bool {
	return !IsPublicInstance && sc == ""
}

func (sc StoreCode) IsP2P() bool {
	return sc == "p2p"
}

type Store struct {
	Code  StoreCode `json:"c"`
	Token string    `json:"t"`
}

type UserDataStores struct {
	Stores           []Store         `json:"stores"`
	stores           []resolvedStore `json:"-"`
	isStremThruStore bool            `json:"-"`
	isP2P            bool            `json:"-"`
}

func (ud UserDataStores) HasRequiredValues() bool {
	storeCount := len(ud.Stores)
	if storeCount == 0 {
		return false
	}
	for i := range ud.Stores {
		s := &ud.Stores[i]
		if (s.Code.IsStremThru() || s.Code.IsP2P()) && storeCount > 1 {
			return false
		}
		if !s.Code.IsP2P() && s.Token == "" {
			return false
		}
	}
	return true
}

func (ud UserDataStores) StripSecrets() UserDataStores {
	ud.Stores = slices.Clone(ud.Stores)
	for i := range ud.Stores {
		s := &ud.Stores[i]
		s.Token = ""
	}
	return ud
}

func (ud *UserDataStores) IsStremThruStore() bool {
	return ud.isStremThruStore
}

func (ud *UserDataStores) IsP2P() bool {
	return ud.isP2P
}

func (ud *UserDataStores) Prepare(ctx *stremio_shared.Ctx) (err error, errField string) {
	storeCount := len(ud.Stores)
	if storeCount == 0 {
		return errors.New("missing store"), "store"
	}
	if storeCount == 1 && ud.Stores[0].Code.IsStremThru() {
		token := ud.Stores[0].Token
		auth, err := util.ParseBasicAuth(token)
		if err != nil {
			return err, "token"
		}
		password := config.Auth.GetPassword(auth.Username)
		if password == "" || password != auth.Password {
			return errors.New("invalid token"), "token"
		} else {
			ctx.IsProxyAuthorized = true
			ctx.ProxyAuthUser = auth.Username
			ctx.ProxyAuthPassword = auth.Password
		}

		storeNames := config.StoreAuthToken.ListStores(auth.Username)
		stores := make([]resolvedStore, len(storeNames))
		for i, storeName := range storeNames {
			stores[i] = resolvedStore{
				Store:     shared.GetStore(storeName),
				AuthToken: config.StoreAuthToken.GetToken(ctx.ProxyAuthUser, storeName),
			}
		}
		ud.stores = stores
		ud.isStremThruStore = true
	} else if storeCount == 1 && ud.Stores[0].Code.IsP2P() {
		ud.stores = nil
		ud.isP2P = true
		return nil, ""
	} else {
		stores := make([]resolvedStore, storeCount)
		for i := range ud.Stores {
			s := &ud.Stores[i]
			stores[i] = resolvedStore{
				Store:     shared.GetStore(string(store.StoreCode(s.Code).Name())),
				AuthToken: s.Token,
			}
		}
		ud.stores = stores
	}
	return nil, ""
}

func (ud *UserDataStores) HasStores() bool {
	return len(ud.stores) > 0
}

func (ud *UserDataStores) GetStores() []resolvedStore {
	return ud.stores
}

func (ud *UserDataStores) GetStoreByIdx(idx int) *resolvedStore {
	return &ud.stores[idx]
}

func (ud *UserDataStores) GetStoreByCode(code string) *resolvedStore {
	if len(ud.stores) == 1 {
		return &ud.stores[0]
	}
	storeCode := store.StoreCode(strings.ToLower(code))
	for i := range ud.stores {
		us := &ud.stores[i]
		if us.Store.GetName().Code() == storeCode {
			return us
		}
	}
	return &ud.stores[0]
}

type resolvedStore struct {
	Store     store.Store
	AuthToken string
}

type storesResult[T any] struct {
	Data   []T
	Err    []error
	HasErr bool
}

func (ud *UserDataStores) GetUser() storesResult[*store.User] {
	ms := ud.stores

	count := len(ms)
	res := storesResult[*store.User]{
		Data:   make([]*store.User, count),
		Err:    make([]error, count),
		HasErr: false,
	}

	var wg sync.WaitGroup
	for i := range ms {
		wg.Add(1)
		s := &ms[i]
		go func() {
			defer wg.Done()
			if s.Store == nil {
				res.Err[i] = errors.New("invalid userdata, invalid store")
				res.HasErr = true
				return
			}
			params := &store.GetUserParams{}
			params.APIKey = s.AuthToken
			res.Data[i], res.Err[i] = s.Store.GetUser(params)
			if res.Err[i] != nil {
				res.HasErr = true
			}
		}()
	}
	wg.Wait()

	return res
}

type magnetFileValidation struct {
	viable     bool
	invalid    bool
	disallowed bool
}

func validateMagnetFiles(item store.CheckMagnetDataItem, allowMixed bool) magnetFileValidation {
	// No file list was supplied by the provider.
	// Preserve the existing cached-status behavior.
	if len(item.Files) == 0 {
		return magnetFileValidation{viable: item.Status == store.MagnetStatusCached}
	}

	// Validate supplied files independently of cache status. Only cached results
	// can supply a cached hit, but misses can still supply rejection evidence.
	// Strict instances reject any blocked file; all instances require an allowed video.
	hasVideo := false
	for _, file := range item.Files {
		classification := core.ClassifyFile(file.Name, file.Path)
		if classification.Disallowed {
			if !allowMixed {
				return magnetFileValidation{invalid: true, disallowed: true}
			}
			continue
		}
		if classification.AllowedVideo {
			hasVideo = true
		}
	}

	return magnetFileValidation{viable: hasVideo && item.Status == store.MagnetStatusCached, invalid: !hasVideo}
}

type storesCheckMagnetData struct {
	ByHash map[string]string
	// FilesByHash contains the supplied lists of the selected, validated cached
	// answers. Missing lists are absent, so they cannot clear stored no-video evidence.
	FilesByHash       map[string][]store.MagnetFile
	InvalidByHash     map[string]struct{}
	Err               []error
	HasErr            bool
	HasErrByStoreCode map[string]struct{}
	m                 sync.Mutex
}

func (ud *UserDataStores) CheckMagnet(params *store.CheckMagnetParams, log *logger.Logger) *storesCheckMagnetData {
	ms := ud.stores

	storeCount := len(ms)
	res := storesCheckMagnetData{
		ByHash:            map[string]string{},
		FilesByHash:       map[string][]store.MagnetFile{},
		InvalidByHash:     map[string]struct{}{},
		Err:               make([]error, storeCount),
		HasErr:            false,
		HasErrByStoreCode: map[string]struct{}{},
	}

	if storeCount == 0 {
		res.Err = []error{errors.New("no configured store")}
		res.HasErr = true
		return &res
	}

	magnets := core.NormalizeMagnetHashes(params.Magnets)
	firstStore := ms[0]
	if firstStore.Store == nil {
		res.Err[0] = errors.New("invalid userdata, invalid store")
		res.HasErr = true
		return &res
	}

	missingHashes := []string{}
	// With mixed torrents disabled, a blocked file rejects the hash across checked
	// stores. With mixed torrents enabled, blocked files are skipped and an allowed
	// video list can make the hash viable; this map stays empty.
	disallowedByHash := map[string]struct{}{}
	allowMixed := config.AllowVideoTorrentsWithDisallowedFiles

	cmParams := &store.CheckMagnetParams{
		Magnets:   magnets,
		ClientIP:  params.ClientIP,
		SId:       params.SId,
		LocalOnly: params.LocalOnly,
	}
	cmParams.APIKey = firstStore.AuthToken
	storeName := firstStore.Store.GetName()
	storeCode := strings.ToUpper(string(storeName.Code()))
	if cmRes, err := firstStore.Store.CheckMagnet(cmParams); err != nil {
		log.Warn("failed to check magnet", "error", err, "store.name", firstStore.Store.GetName())
		res.Err[0] = err
		res.HasErr = true
		res.HasErrByStoreCode[storeCode] = struct{}{}
		if storeCount > 1 {
			missingHashes = magnets
		}
	} else {
		if cmRes.RecheckError != nil {
			log.Warn("failed to recheck missing file lists; retaining reusable results", "error", cmRes.RecheckError, "store.name", storeName)
			res.Err[0] = cmRes.RecheckError
			res.HasErr = true
			res.HasErrByStoreCode[storeCode] = struct{}{}
		}
		returnedHashes := map[string]struct{}{}
		for _, item := range store_util.MergeCheckMagnetItems(cmRes.Items) {
			returnedHashes[item.Hash] = struct{}{}
			validation := validateMagnetFiles(item, allowMixed)
			if storeCount > 1 && !validation.disallowed && (!validation.viable || (config.StoreRecheckMissingFileLists && config.StoreRecheckMissingFileListsCrossStore && len(item.Files) == 0)) {
				missingHashes = append(missingHashes, item.Hash)
			}
			if validation.disallowed {
				disallowedByHash[item.Hash] = struct{}{}
				delete(res.ByHash, item.Hash)
				res.InvalidByHash[item.Hash] = struct{}{}
				log.Trace("magnet has disallowed files", "hash", item.Hash, "sid", params.SId, "store.name", storeName, "file_count", len(item.Files))
				continue
			}
			if _, disallowed := disallowedByHash[item.Hash]; disallowed {
				continue
			}
			if validation.viable {
				res.ByHash[item.Hash] = storeCode
				if len(item.Files) > 0 {
					res.FilesByHash[item.Hash] = item.Files
				}
			} else if validation.invalid {
				res.InvalidByHash[item.Hash] = struct{}{}
				log.Trace("magnet has no allowed video files", "hash", item.Hash, "sid", params.SId, "store.name", storeName, "file_count", len(item.Files))
			}
		}
		// A successful response can omit misses. They still need ordinary fallback.
		if storeCount > 1 {
			for _, hash := range magnets {
				if _, returned := returnedHashes[hash]; !returned {
					missingHashes = append(missingHashes, hash)
					returnedHashes[hash] = struct{}{}
				}
			}
		}
	}

	if storeCount == 1 || len(missingHashes) == 0 {
		return &res
	}

	var wg sync.WaitGroup
	// Check fallback stores for misses and no-video validation failures; recheck
	// cached results without a file list only when both recheck switches are enabled.
	// Strict disallowed evidence is definitive and does not schedule fallback.
	for i := range storeCount - 1 {
		idx := i + 1
		s := &ms[idx]

		wg.Go(func() {
			if s.Store == nil {
				res.m.Lock()
				res.Err[idx] = errors.New("invalid userdata, invalid store")
				res.HasErr = true
				res.m.Unlock()
				return
			}
			cmParams := &store.CheckMagnetParams{
				Magnets:   missingHashes,
				ClientIP:  params.ClientIP,
				SId:       params.SId,
				LocalOnly: params.LocalOnly,
			}
			cmParams.APIKey = s.AuthToken
			cmRes, err := s.Store.CheckMagnet(cmParams)
			storeName := s.Store.GetName()
			storeCode := strings.ToUpper(string(storeName.Code()))
			if err != nil {
				log.Warn("failed to check magnet", "error", err, "store.name", s.Store.GetName())
				res.m.Lock()
				res.Err[idx] = err
				res.HasErr = true
				res.HasErrByStoreCode[storeCode] = struct{}{}
				res.m.Unlock()
			} else {
				res.m.Lock()
				defer res.m.Unlock()
				if cmRes.RecheckError != nil {
					log.Warn("failed to recheck missing file lists; retaining reusable results", "error", cmRes.RecheckError, "store.name", storeName)
					res.Err[idx] = cmRes.RecheckError
					res.HasErr = true
					res.HasErrByStoreCode[storeCode] = struct{}{}
				}

				for _, item := range store_util.MergeCheckMagnetItems(cmRes.Items) {
					validation := validateMagnetFiles(item, allowMixed)
					if validation.disallowed {
						disallowedByHash[item.Hash] = struct{}{}
						delete(res.ByHash, item.Hash)
						delete(res.FilesByHash, item.Hash)
						res.InvalidByHash[item.Hash] = struct{}{}
						log.Trace("magnet has disallowed files", "hash", item.Hash, "sid", params.SId, "store.name", storeName, "file_count", len(item.Files))
						continue
					}
					if _, disallowed := disallowedByHash[item.Hash]; disallowed {
						continue
					}
					if validation.viable {
						if _, invalid := res.InvalidByHash[item.Hash]; invalid && len(item.Files) == 0 {
							continue
						}
						if _, found := res.ByHash[item.Hash]; !found || (len(res.FilesByHash[item.Hash]) == 0 && len(item.Files) > 0) {
							res.ByHash[item.Hash] = storeCode
							if len(item.Files) > 0 {
								res.FilesByHash[item.Hash] = item.Files
							}
							if _, invalid := res.InvalidByHash[item.Hash]; invalid {
								log.Trace("cached magnet became viable on another store", "hash", item.Hash, "sid", params.SId, "store.name", storeName)
								delete(res.InvalidByHash, item.Hash)
							}
						}
					} else if validation.invalid {
						if len(res.FilesByHash[item.Hash]) == 0 {
							delete(res.ByHash, item.Hash)
							res.InvalidByHash[item.Hash] = struct{}{}
							log.Trace("magnet has no allowed video files", "hash", item.Hash, "sid", params.SId, "store.name", storeName, "file_count", len(item.Files))
						}
					}
				}
			}
		})
	}
	wg.Wait()

	return &res
}

type storesCheckNewzData struct {
	ByHash            map[string]string
	Err               []error
	HasErr            bool
	HasErrByStoreCode *util.Set[string]
	m                 sync.Mutex
}

func (ud *UserDataStores) CheckNewz(params *store.CheckNewzParams, log *logger.Logger) *storesCheckNewzData {
	ms := make([]resolvedStore, 0, len(ud.stores))
	for _, s := range ud.stores {
		if _, ok := s.Store.(store.NewzStore); ok {
			ms = append(ms, s)
		}
	}

	storeCount := len(ms)
	res := storesCheckNewzData{
		ByHash:            map[string]string{},
		Err:               make([]error, storeCount),
		HasErr:            false,
		HasErrByStoreCode: util.NewSet[string](),
	}

	if storeCount == 0 {
		res.Err = []error{errors.New("no configured store")}
		res.HasErr = true
		return &res
	}

	firstStore := ms[0]

	missingHashes := []string{}

	cnParams := &store.CheckNewzParams{
		Hashes: params.Hashes,
	}
	cnParams.APIKey = firstStore.AuthToken
	storeCode := strings.ToUpper(string(firstStore.Store.GetName().Code()))
	if cnRes, err := firstStore.Store.(store.NewzStore).CheckNewz(cnParams); err != nil {
		log.Warn("failed to check magnet", "error", err, "store.name", firstStore.Store.GetName())
		res.Err[0] = err
		res.HasErr = true
		res.HasErrByStoreCode.Add(storeCode)

		if storeCount > 1 {
			missingHashes = params.Hashes
		}
	} else {
		for i := range cnRes.Items {
			item := cnRes.Items[i]
			if item.Status == store.NewzStatusCached {
				res.ByHash[item.Hash] = storeCode
			} else if storeCount > 1 {
				missingHashes = append(missingHashes, item.Hash)
			}
		}
	}

	if storeCount == 1 || len(missingHashes) == 0 {
		return &res
	}

	var wg sync.WaitGroup
	for i := range storeCount - 1 {
		idx := i + 1
		s := &ms[idx]

		wg.Go(func() {
			if s.Store == nil {
				res.Err[idx] = errors.New("invalid userdata, invalid store")
				res.HasErr = true
				return
			}
			cnParams := &store.CheckNewzParams{
				Hashes: missingHashes,
			}
			cnParams.APIKey = s.AuthToken
			cnRes, err := s.Store.(store.NewzStore).CheckNewz(cnParams)
			storeCode := strings.ToUpper(string(s.Store.GetName().Code()))
			if err != nil {
				log.Warn("failed to check magnet", "error", err, "store.name", s.Store.GetName())
				res.Err[idx] = err
				res.HasErr = true
				res.HasErrByStoreCode.Add(storeCode)
			} else {
				res.m.Lock()
				defer res.m.Unlock()

				for _, item := range cnRes.Items {
					if _, found := res.ByHash[item.Hash]; !found && item.Status == store.NewzStatusCached {
						res.ByHash[item.Hash] = storeCode
					}
				}
			}
		})
	}
	wg.Wait()

	return &res
}
