package store_util

import (
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
	Found      map[string]store.CheckMagnetDataItem
	Reusable   map[string]store.CheckMagnetDataItem
	Rechecking bool
}

type PartitionCheckMagnetItemsParams struct {
	Items                   []store.CheckMagnetDataItem
	RecheckMissingFileLists bool
	LocalOnly               bool
}

// PartitionCheckMagnetItems merges duplicate responses and separates answers
// that can be reused immediately from cached answers retained as recheck fallback.
func PartitionCheckMagnetItems(params PartitionCheckMagnetItemsParams) CheckMagnetItemsPartition {
	partition := CheckMagnetItemsPartition{
		Found:    make(map[string]store.CheckMagnetDataItem, len(params.Items)),
		Reusable: make(map[string]store.CheckMagnetDataItem, len(params.Items)),
	}
	for _, item := range MergeCheckMagnetItems(params.Items) {
		partition.Reusable[item.Hash] = item
		if params.RecheckMissingFileLists && !params.LocalOnly && item.Status == store.MagnetStatusCached && len(item.Files) == 0 {
			partition.Rechecking = true
			continue
		}
		partition.Found[item.Hash] = item
	}
	return partition
}

// Restore preserves reusable answers when an additional file-list recheck fails.
// Unresolved hashes remain unknown. Ordinary lookup failures are returned.
func (p CheckMagnetItemsPartition) Restore(hashes []string, err error) (*store.CheckMagnetData, error) {
	if !p.Rechecking {
		return nil, err
	}
	data := &store.CheckMagnetData{Items: make([]store.CheckMagnetDataItem, 0, len(hashes)), RecheckError: err}
	for _, hash := range hashes {
		item, found := p.Reusable[hash]
		if !found {
			item = store.CheckMagnetDataItem{Hash: hash, Magnet: "magnet:?xt=urn:btih:" + hash, Status: store.MagnetStatusUnknown, Files: []store.MagnetFile{}}
		}
		data.Items = append(data.Items, item)
	}
	return data, nil
}
