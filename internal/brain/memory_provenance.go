package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/models"
)

// Provenance is derived from existing source records on every read. This also
// repairs the projection of older facts without inventing duplicate manual links.
func (b *Brain) appendGoalMapFactSources(ctx context.Context, namespaceID int64, result *models.GoalMap) error {
	factIDs, episodeIDs := []int64{}, []int64{}
	seen := make(map[string]bool)
	for _, memory := range result.Memories {
		seen[memory.Key] = true
		if memory.MemoryType == "fact" {
			factIDs = append(factIDs, memory.MemoryID)
		}
		if memory.MemoryType == "episode" {
			episodeIDs = append(episodeIDs, memory.MemoryID)
		}
	}
	if len(factIDs)+len(episodeIDs) == 0 {
		return nil
	}
	rows, err := b.pool.Query(ctx, `
		SELECT fact.id, left(fact.content, $4), char_length(fact.content) > $4,
		       episode.id, left(episode.content, $4), char_length(episode.content) > $4
		FROM fact_sources source
		JOIN facts fact ON fact.id = source.fact_id
		JOIN episodes episode ON episode.id = source.episode_id
		WHERE (source.fact_id = ANY($2) OR source.episode_id = ANY($3))
		  AND fact.namespace_id = $1 AND episode.namespace_id = $1
		  AND fact.deleted_at IS NULL AND fact.valid_until IS NULL AND episode.deleted_at IS NULL
		ORDER BY fact.id, episode.id`, namespaceID, factIDs, episodeIDs, goalMapMemoryContentLimit)
	if err != nil {
		return fmt.Errorf("read memory sources: %w", err)
	}
	defer rows.Close()
	directEdges := append([]models.GoalMapEdge(nil), result.Edges...)
	edges := make(map[string]bool)
	for _, edge := range directEdges {
		edges[edge.From+":"+edge.To+":"+edge.Relation] = true
	}
	appendEdge := func(edge models.GoalMapEdge) {
		key := edge.From + ":" + edge.To + ":" + edge.Relation
		if edges[key] {
			return
		}
		edges[key] = true
		edge.Key = "memory-edge:" + key
		result.Edges = append(result.Edges, edge)
	}
	for rows.Next() {
		fact := models.GoalMapMemory{MemoryType: "fact", Status: "active"}
		episode := models.GoalMapMemory{MemoryType: "episode", Status: "recorded"}
		if err := rows.Scan(&fact.MemoryID, &fact.Content, &fact.ContentTruncated, &episode.MemoryID, &episode.Content, &episode.ContentTruncated); err != nil {
			return err
		}
		fact.Key, episode.Key = goalMapMemoryKey("fact", fact.MemoryID), goalMapMemoryKey("episode", episode.MemoryID)
		for _, memory := range []models.GoalMapMemory{fact, episode} {
			if !seen[memory.Key] {
				result.Memories = append(result.Memories, memory)
				seen[memory.Key] = true
			}
		}
		appendEdge(models.GoalMapEdge{From: fact.Key, To: episode.Key, Relation: "derived_from", Derived: true})
		for _, edge := range directEdges {
			if edge.From == episode.Key && (strings.HasPrefix(edge.To, "work:") || strings.HasPrefix(edge.To, "goal:")) {
				edge.From, edge.Derived = fact.Key, true
				appendEdge(edge)
			} else if edge.To == episode.Key && (strings.HasPrefix(edge.From, "work:") || strings.HasPrefix(edge.From, "goal:")) {
				edge.To, edge.Derived = fact.Key, true
				appendEdge(edge)
			}
		}
	}
	return rows.Err()
}
