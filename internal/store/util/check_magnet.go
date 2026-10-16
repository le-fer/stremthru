package store_util

import (
	"errors"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/store"
)

// MergeCheckMagnetItems combines duplicate canonical hashes before reuse or
// validation. All supplied files survive, including conflicting names and paths.
// A cached answer takes precedence over a miss; otherwise the first answer's
// metadata is retained. Hash order follows the first appearance in the response.
func MergeCheckMagnetItems(items []store.CheckMagnetDataItem) []store.CheckMagnetDataItem {
	merged := make([]store.CheckMagnetDataItem, 0, len(items))
	type mergeState struct {
		index     int
		ownsFiles bool
	}
	indexByHash := make(map[string]mergeState, len(items))
	for _, item := range items {
		item.Hash = core.NormalizeMagnetHash(item.Hash)
		if item.Hash == "" {
			continue
		}
		if state, found := indexByHash[item.Hash]; found {
			previous := &merged[state.index]
			// Take ownership on the first duplicate only. Singletons stay zero-copy;
			// later siblings grow owned storage instead of recopying every prefix.
			// Never append into a caller-owned backing array, even with spare capacity.
			files := previous.Files
			if !state.ownsFiles {
				files = make([]store.MagnetFile, len(previous.Files), len(previous.Files)+len(item.Files))
				copy(files, previous.Files)
				state.ownsFiles = true
				indexByHash[item.Hash] = state
			}
			files = append(files, item.Files...)
			if previous.Status != store.MagnetStatusCached && item.Status == store.MagnetStatusCached {
				*previous = item
			}
			previous.Files = files
		} else {
			indexByHash[item.Hash] = mergeState{index: len(merged)}
			merged = append(merged, item)
		}
	}
	return merged
}

type CheckMagnetItemsPartition struct {
	Found        map[string]store.CheckMagnetDataItem
	Reusable     map[string]store.CheckMagnetDataItem
	Rechecking   bool
	RecheckError error
}

type PartitionCheckMagnetItemsParams struct {
	Items                   []store.CheckMagnetDataItem
	RecheckMissingFileLists bool
	LocalOnly               bool
	RecheckError            error
}

func (p CheckMagnetItemsPartition) IsFileListRecheck(hash string, crossStore map[string]struct{}) bool {
	if _, ok := crossStore[hash]; ok {
		return true
	}
	item, reusable := p.Reusable[hash]
	_, found := p.Found[hash]
	return !found && reusable && item.Status == store.MagnetStatusCached && len(item.Files) == 0
}

func (p CheckMagnetItemsPartition) FileListRecheckCount(hashes []string, crossStore map[string]struct{}) int {
	count := 0
	for _, hash := range hashes {
		if p.IsFileListRecheck(hash, crossStore) {
			count++
		}
	}
	return count
}

// PartitionCheckMagnetItems merges duplicate responses and separates answers
// that can be reused immediately from cached answers retained as recheck fallback.
func PartitionCheckMagnetItems(params PartitionCheckMagnetItemsParams) CheckMagnetItemsPartition {
	partition := CheckMagnetItemsPartition{
		Found:        make(map[string]store.CheckMagnetDataItem, len(params.Items)),
		Reusable:     make(map[string]store.CheckMagnetDataItem, len(params.Items)),
		RecheckError: params.RecheckError,
	}
	for _, item := range MergeCheckMagnetItems(params.Items) {
		partition.Reusable[item.Hash] = item
		// After a peer recheck failure, keep returned answers while allowing the
		// adapter to look up hashes omitted by other peer chunks.
		if params.RecheckError == nil && params.RecheckMissingFileLists && !params.LocalOnly && item.Status == store.MagnetStatusCached && len(item.Files) == 0 {
			partition.Rechecking = true
			continue
		}
		partition.Found[item.Hash] = item
	}
	return partition
}

// Restore preserves reusable answers after a failed local recheck or a lookup
// following a peer recheck failure. Unresolved hashes remain unknown; ordinary
// lookup failures without either recheck condition are returned.
func (p CheckMagnetItemsPartition) Restore(hashes []string, err error) (*store.CheckMagnetData, error) {
	if !p.Rechecking && p.RecheckError == nil {
		return nil, err
	}
	data := &store.CheckMagnetData{Items: make([]store.CheckMagnetDataItem, 0, len(hashes)), RecheckError: errors.Join(p.RecheckError, err)}
	for _, hash := range hashes {
		item, found := p.Reusable[hash]
		if !found {
			item = store.CheckMagnetDataItem{Hash: hash, Magnet: "magnet:?xt=urn:btih:" + hash, Status: store.MagnetStatusUnknown, Files: []store.MagnetFile{}}
		}
		data.Items = append(data.Items, item)
	}
	return data, nil
}
