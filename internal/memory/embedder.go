package memory

import "github.com/BackendStack21/odek/internal/embedding"

// The text-embedding backends now live in internal/embedding so sessions and
// skills can share them. Memory keeps these package-local aliases and thin
// constructors so its existing call sites (and the on-disk config shape) are
// unchanged: EmbeddingConfig still names memory's "embedding" config block, and
// textEmbedder is still the seam every retrieval path fits and embeds against.

// EmbeddingConfig is memory's embedding backend selector. See embedding.Config.
type EmbeddingConfig = embedding.Config

// textEmbedder is the embedding seam shared with sessions and skills.
type textEmbedder = embedding.TextEmbedder

// newTextEmbedder builds the embedder selected by cfg, falling back to a
// RandomProjections embedder of rpDims dimensions. See embedding.New.
func newTextEmbedder(cfg *EmbeddingConfig, rpDims int) textEmbedder {
	return embedding.New(cfg, rpDims)
}

// newRPTextEmbedder builds a RandomProjections embedder of the given
// dimensionality. See embedding.NewRP.
func newRPTextEmbedder(dims int) textEmbedder {
	return embedding.NewRP(dims)
}
