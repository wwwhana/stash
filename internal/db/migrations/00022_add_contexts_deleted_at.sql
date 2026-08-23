-- +goose Up
-- contexts 만 유일하게 하드 DELETE 로 지워지고 있었다.
-- 다른 엔티티(episodes·facts·goals·hypotheses·failures·relationships·patterns·causal_links)는
-- 전부 deleted_at 소프트 삭제라, 삭제 의미가 테이블마다 달랐다.
-- 임시 데이터라도 "무엇을 언제 지웠는가"는 남는 편이 낫다 — 지워진 컨텍스트는
-- 그 시점에 무엇에 집중하고 있었는지를 말해주는 기록이기도 하다.
ALTER TABLE contexts ADD COLUMN deleted_at TIMESTAMPTZ NULL;

-- 살아 있는 컨텍스트 조회를 위한 부분 인덱스.
CREATE INDEX IF NOT EXISTS contexts_active_idx ON contexts (namespace_id) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS contexts_active_idx;
ALTER TABLE contexts DROP COLUMN IF EXISTS deleted_at;
