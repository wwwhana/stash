-- +goose Up
-- 하이브리드 검색용 키워드 인덱스.
--
-- 벡터 검색만으로는 식별자(FROMM-4414, HOOKVERIFY 등)를 잘 잡지 못한다.
-- 임베딩은 의미를 담지 고유 토큰을 담지 않기 때문이다.
-- trigram 을 쓰는 이유: to_tsvector 는 한국어 토크나이저가 없어 형태소가 뭉개지는데,
-- trigram 은 언어 중립적이고 부분 문자열 매칭에 강해 한글·영문 식별자 모두에 통한다.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS episodes_content_trgm_idx ON episodes USING gin (content gin_trgm_ops);
CREATE INDEX IF NOT EXISTS facts_content_trgm_idx ON facts USING gin (content gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS facts_content_trgm_idx;
DROP INDEX IF EXISTS episodes_content_trgm_idx;
