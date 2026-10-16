package stremio_wrap

import (
	"github.com/MunifTanjim/stremthru/internal/shared"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
)

type Ctx = stremio_shared.Ctx

var IsMethod = shared.IsMethod
var SendError = shared.SendError
var ExtractRequestBaseURL = shared.ExtractRequestBaseURL

var SendResponse = stremio_shared.SendResponse
var SendHTML = stremio_shared.SendHTML

func dedupeStreams(allStreams []WrappedStream) []WrappedStream {
	// Pending Torz candidates cannot consume siblings until file matching resolves.
	deferredHashes := map[string]struct{}{}
	for i := range allStreams {
		if allStreams[i].deferredTorzFiles {
			if hash := wrappedStreamHash(&allStreams[i]); hash != "" {
				deferredHashes[hash] = struct{}{}
			}
		}
	}
	hashSeen := map[string]struct{}{}

	streams := []WrappedStream{}
	for i := range allStreams {
		s := allStreams[i]
		if s.r != nil && s.r.Hash != "" {
			hash := wrappedStreamHash(&s)
			if hash == "" {
				streams = append(streams, s)
				continue
			}
			if _, deferred := deferredHashes[hash]; deferred {
				streams = append(streams, s)
				continue
			}
			if _, seen := hashSeen[hash]; seen {
				continue
			} else {
				hashSeen[hash] = struct{}{}
			}
		}
		streams = append(streams, s)
	}
	return streams
}
