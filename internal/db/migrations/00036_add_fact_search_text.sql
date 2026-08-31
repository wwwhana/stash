-- +goose Up
-- 사실의 원문만 검색하면 구조화된 entity/property/value 값에 있는
-- 식별자를 찾지 못한다. 저장 시 자동으로 합친 검색 텍스트를 유지해
-- 원문과 구조화된 필드를 같은 trigram 검색 대상으로 만든다.
ALTER TABLE facts
    ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
        COALESCE(content, '') || ' ' ||
        COALESCE(entity, '') || ' ' ||
        COALESCE(property, '') || ' ' ||
        COALESCE(value, '')
    ) STORED;

CREATE INDEX facts_search_text_trgm_idx
    ON facts USING gin (search_text gin_trgm_ops)
    WHERE deleted_at IS NULL AND valid_until IS NULL;

-- +goose Down
DROP INDEX IF EXISTS facts_search_text_trgm_idx;
ALTER TABLE facts DROP COLUMN IF EXISTS search_text;
