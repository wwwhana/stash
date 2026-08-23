-- +goose Up
-- 인과 링크 추출(Stage 3.5)이 관계 추출(Stage 2)과 last_fact_id 를 공유해
-- 사실상 항상 0건이었다. 관계 단계가 성공하면 그 커서를 최신 fact 까지 밀어버리고,
-- 인과 단계는 그 뒤에 `WHERE id > last_fact_id` 로 조회하므로 언제나 빈 결과를 받는다.
-- 그리고 `len(facts) < 2` 조기 종료에 걸린다 — 관계 추출이 실패해야만 인과가 도는 역설.
--
-- pattern·goal·hypothesis 는 전용 fact 커서를 갖고 있는데(last_pattern_fact_id,
-- last_goal_progress_fact_id, last_hypothesis_fact_id) 인과만 누락돼 있었다.
-- 같은 규칙으로 전용 커서를 준다.
--
-- 기본값 0 이라, 이미 쌓인 fact 는 다음 consolidation 에서 처음부터 인과 추출 대상이 된다.
ALTER TABLE consolidation_progress ADD COLUMN last_causal_fact_id BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE consolidation_progress DROP COLUMN IF EXISTS last_causal_fact_id;
