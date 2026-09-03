# Completion evidence

Evidence records what was observed. It must not claim a broader path than the one exercised.

## Submit useful evidence

Use `submit_work_evidence` after observing a result. Choose a short `evidence_type` such as `test`, `build`, `http`, `review`, `artifact`, `ui`, or `observation`. In `summary`, state the checked path and outcome. Use `reference` for a command, file, run, URL, or artifact identifier, and `payload` for small structured details such as an exit code or status.

Never include credentials, lease tokens, private headers, or unrelated response bodies.

## Verify each condition

Use one `submit_work_evidence` call for all conditions proved by the same observation. Put the successfully proved pending IDs in `finish_work.passed_condition_ids`; it accepts them only when the current attempt supplied linked evidence. Use `verify_work_condition` only for an explicit waiver or when acceptance must be recorded before finish. A source review does not prove a runtime path; a successful build does not prove UI behavior; a headless test does not prove a physical device write.

For `status: passed`, attach evidence that directly demonstrates the condition. For `status: waived`, attach supporting evidence and explain the exact reason in `waiver_reason`.

`remember_work` keeps durable context searchable but does not attach evidence to a completion condition. `checkpoint_work` records progress but also does not verify a condition.

## Finish only after acceptance

Call `resume_work` when condition state is unclear. Completion is established only when Stash accepts `finish_work`; a local assumption, status label, or rejected finish call leaves the item unfinished.
