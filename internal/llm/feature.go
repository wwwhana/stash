// Package llm routes each server feature to a configured model provider.
// Providers and their assignments live in PostgreSQL and can be changed at
// runtime; the STASH_OPENAI_* environment stays available as a fallback.
package llm

import "fmt"

// Kind separates the two client shapes a feature can need.
type Kind string

const (
	KindEmbedding Kind = "embedding"
	KindReasoning Kind = "reasoning"
)

// Feature names one server capability that calls a model.
type Feature string

const (
	// FeatureEmbedding vectorizes memories, facts, and search queries.
	FeatureEmbedding Feature = "embedding"
	// FeatureConsolidation covers every reasoning stage of the consolidation
	// pipeline: facts, relationships, patterns, contradictions, causal links,
	// goal progress, failure patterns, and hypothesis evidence.
	FeatureConsolidation Feature = "consolidation"
	// FeaturePlanValidation is the explicit semantic review of a work plan.
	FeaturePlanValidation Feature = "plan_validation"
	// FeatureWiki authors and lints wiki pages on the server side.
	FeatureWiki Feature = "wiki"
)

// FeatureInfo describes a feature for operators and the admin console.
type FeatureInfo struct {
	Feature     Feature `json:"feature"`
	Kind        Kind    `json:"kind"`
	Description string  `json:"description"`
}

var featureCatalog = []FeatureInfo{
	{FeatureEmbedding, KindEmbedding, "Vectors for memories, facts, wiki pages, and search queries."},
	{FeatureConsolidation, KindReasoning, "Fact extraction and every other consolidation stage."},
	{FeaturePlanValidation, KindReasoning, "Semantic review of the living work plan."},
	{FeatureWiki, KindReasoning, "Server-side wiki page drafting and lint."},
}

// Features lists every routable feature in display order.
func Features() []FeatureInfo {
	out := make([]FeatureInfo, len(featureCatalog))
	copy(out, featureCatalog)
	return out
}

// ParseFeature validates a feature name supplied by an operator.
func ParseFeature(raw string) (Feature, error) {
	for _, info := range featureCatalog {
		if string(info.Feature) == raw {
			return info.Feature, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownFeature, raw)
}

// Kind reports which client shape the feature needs.
func (f Feature) Kind() Kind {
	for _, info := range featureCatalog {
		if info.Feature == f {
			return info.Kind
		}
	}
	return ""
}
