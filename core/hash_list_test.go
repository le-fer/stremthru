package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMagnetLinkRejectsInvalidHashes(t *testing.T) {
	for _, value := range []string{
		"invalid",
		strings.Repeat("z", 40),
		"magnet:?xt=urn:btih:invalid",
		"magnet:?xt=urn:btih:" + strings.Repeat("z", 40),
	} {
		t.Run(value, func(t *testing.T) {
			magnet, err := ParseMagnetLink(value)
			assert.Error(t, err)
			assert.Empty(t, magnet.Hash)
		})
	}
}

func TestNormalizeMagnetHashesKeepsOrderWithoutMutatingInputs(t *testing.T) {
	const hash = "db42af2b6ae098558b98a14cd7874ef64162cac8"
	const other = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values := []string{"DB42AF2B6AE098558B98A14CD7874EF64162CAC8", other, "3NBK6K3K4CMFLC4YUFGNPB2O6ZAWFSWI", "magnet:?xt=urn:btih:" + hash, "invalid", strings.Repeat("z", 40), "magnet:?xt=urn:btmh:unsupported"}
	original := append([]string(nil), values...)
	assert.Equal(t, []string{hash, other}, NormalizeMagnetHashes(values))
	assert.Equal(t, original, values)
	assert.Empty(t, NormalizeMagnetHashes(nil))
}

func TestParseMagnetLinkFindsSupportedHashInHybridMagnet(t *testing.T) {
	const hash = "db42af2b6ae098558b98a14cd7874ef64162cac8"
	raw := "magnet:?xt=urn:btmh:1220" + strings.Repeat("ab", 32) + "&xt=URN:BTIH:" + strings.ToUpper(hash)
	magnet, err := ParseMagnetLink(raw)
	assert.NoError(t, err)
	assert.Equal(t, hash, magnet.Hash)
	assert.Equal(t, raw, magnet.RawLink, "forwarding must retain both hybrid exact topics")
	assert.Equal(t, "magnet:?xt=urn:btih:"+hash, magnet.Link, "the canonical operational link remains v1-compatible")
}

func TestIsMagnetHashV2(t *testing.T) {
	assert.True(t, IsMagnetHashV2(strings.Repeat("aB", 32)))
	assert.False(t, IsMagnetHashV2(strings.Repeat("a", 40)))
	assert.False(t, IsMagnetHashV2(strings.Repeat("z", 64)))
}
