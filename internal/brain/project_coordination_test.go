package brain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
)

func TestNormalizeWorkCapabilities(t *testing.T) {
	got, err := NormalizeWorkCapabilities([]string{"Browser", " code ", "browser", "data_api", ""})
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(got, ","); joined != "browser,code,data_api" {
		t.Fatalf("capabilities = %q", joined)
	}
	for _, values := range [][]string{{"Not Valid"}, {"1code"}, {strings.Repeat("a", 65)}} {
		if _, err := NormalizeWorkCapabilities(values); err == nil {
			t.Errorf("invalid capabilities were accepted: %#v", values)
		}
	}
}

func TestCompactProjectWorkItemsBoundsRoutingText(t *testing.T) {
	items := compactProjectWorkItems([]models.AgentWorkItem{{
		IssueKey: strings.Repeat("K", 200), Title: strings.Repeat("제목", 500),
		Owner: strings.Repeat("담당", 300), NextAction: strings.Repeat("다음 행동", 500),
	}})
	if len([]rune(items[0].IssueKey)) > 96 || len([]rune(items[0].Title)) > 256 ||
		len([]rune(items[0].Owner)) > 128 || len([]rune(items[0].NextAction)) > 384 {
		t.Fatalf("project work routing text was not bounded: %#v", items[0])
	}
}

func TestNormalizeWorkResourceInputRejectsSecrets(t *testing.T) {
	base := WorkResourceInput{
		ResourceKey: "jira:APP-12", Kind: "ticket", Source: "jira", Authority: "external",
		Title: "APP-12", URI: "https://jira.example.test/browse/APP-12", Role: "input",
		Metadata: json.RawMessage(`{"status":"In Progress"}`),
	}
	got, err := normalizeWorkResourceInput(base)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "jira" || got.Authority != "external" || got.Role != "input" {
		t.Fatalf("normalized resource = %#v", got)
	}

	tests := []struct {
		name   string
		mutate func(*WorkResourceInput)
	}{
		{name: "URI credentials", mutate: func(input *WorkResourceInput) { input.URI = "https://user:pass@example.test/page" }},
		{name: "URI token", mutate: func(input *WorkResourceInput) { input.URI = "https://example.test/page?token=private" }},
		{name: "URI signature", mutate: func(input *WorkResourceInput) { input.URI = "https://example.test/page?X-Amz-Signature=private" }},
		{name: "URI fragment token", mutate: func(input *WorkResourceInput) { input.URI = "https://example.test/page#access_token=private" }},
		{name: "executable URI", mutate: func(input *WorkResourceInput) { input.URI = "javascript:alert(1)" }},
		{name: "nested secret", mutate: func(input *WorkResourceInput) {
			input.Metadata = json.RawMessage(`{"connector":{"api_key":"private"}}`)
		}},
		{name: "array secret", mutate: func(input *WorkResourceInput) {
			input.Metadata = json.RawMessage(`{"headers":[{"authorization":"private"}]}`)
		}},
		{name: "deep array secret", mutate: func(input *WorkResourceInput) {
			input.Metadata = json.RawMessage(`{"groups":[[{"accessToken":"private"}]]}`)
		}},
		{name: "provider token", mutate: func(input *WorkResourceInput) {
			input.Metadata = json.RawMessage(`{"github_token":"private"}`)
		}},
		{name: "client secret", mutate: func(input *WorkResourceInput) {
			input.Metadata = json.RawMessage(`{"client-secret":"private"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if _, err := normalizeWorkResourceInput(input); err == nil {
				t.Fatal("secret-bearing resource was accepted")
			}
		})
	}
}

func TestProjectCoordinationDatabaseFlow(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	var namespaceSlug string
	if err := b.pool.QueryRow(ctx, `SELECT slug FROM namespaces WHERE id = $1`, namespaceID).Scan(&namespaceSlug); err != nil {
		t.Fatalf("read namespace slug: %v", err)
	}

	root, err := b.CreateGoal(ctx, namespaceID, "A를 완성한다", nil, 10)
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	outsideRoot, err := b.CreateGoal(ctx, namespaceID, "다른 목표", nil, 1)
	if err != nil {
		t.Fatalf("CreateGoal outside root: %v", err)
	}
	outside, err := b.CreateWorkItemWithCapabilities(ctx, namespaceID, WorkItemInput{
		GoalID: &outsideRoot.ID, IssueType: "task", Title: "다른 목표의 기존 작업", Description: "공통 목표를 정하기 전에 만든 작업", Status: "ready",
	}, []string{"code"})
	if err != nil {
		t.Fatalf("CreateWorkItemWithCapabilities outside root: %v", err)
	}
	if _, err := b.PrepareWork(ctx, outside.ID, "다른 목표를 처리한다", []CompletionConditionInput{{
		Kind: "custom", Description: "다른 결과를 확인한다", Required: true,
		Verification: json.RawMessage(`{"check":"outside"}`),
	}}, fmt.Sprintf("prepare-outside-%d", outside.ID)); err != nil {
		t.Fatalf("PrepareWork outside root: %v", err)
	}
	if _, err := b.SetProjectGoalRoot(ctx, namespaceID, root.ID, "owner"); err != nil {
		t.Fatalf("SetProjectGoalRoot: %v", err)
	}
	parent, err := b.CreateWorkItemWithCapabilities(ctx, namespaceID, WorkItemInput{
		GoalID: &root.ID, IssueType: "task", Title: "A-1 구현", Description: "부모 작업", Status: "ready", Priority: 10,
	}, []string{"code"})
	if err != nil {
		t.Fatalf("CreateWorkItemWithCapabilities: %v", err)
	}
	parentPreparation, err := b.PrepareWork(ctx, parent.ID, "구현을 시작한다", []CompletionConditionInput{{
		Kind: "test", Description: "전체 확인이 통과한다", Required: true,
		Verification: json.RawMessage(`{"command":"go test ./..."}`),
	}}, fmt.Sprintf("prepare-parent-%d", parent.ID))
	if err != nil {
		t.Fatalf("PrepareWork: %v", err)
	}

	project, err := b.ResumeProject(ctx, namespaceSlug, namespaceID, "", "agent-code", []string{"code"}, true)
	if err != nil {
		t.Fatalf("ResumeProject before claim: %v", err)
	}
	if project.SharedGoal == nil || project.SharedGoal.ID != root.ID || len(project.ReadyWork) != 1 || project.ReadyWork[0].ID != parent.ID {
		t.Fatalf("project resume = %#v", project)
	}
	if noMatch, err := b.ResumeProject(ctx, namespaceSlug, namespaceID, "", "agent-docs", []string{"document"}, true); err != nil || len(noMatch.ReadyWork) != 0 {
		t.Fatalf("capability-filtered resume = %#v, err=%v", noMatch, err)
	}

	attempt, err := b.StartWorkAttempt(ctx, parent.ID, "agent-code", nil, 15*time.Minute, "73996bc5-5458-4ab2-b3e8-bf1d4f0ca537")
	if err != nil {
		t.Fatalf("StartWorkAttempt: %v", err)
	}
	spawnInput := SpawnWorkInput{
		Title: "A-1 자료 조사", Description: "부모 작업에서 발견한 선행 작업", IssueType: "task",
		Relationship: "prerequisite", NextAction: "공식 문서를 확인한다", Capabilities: []string{"research"},
		Conditions: []CompletionConditionInput{{
			Kind: "custom", Description: "필요한 사실을 확인한다", Required: true,
			Verification: json.RawMessage(`{"check":"source noted"}`),
		}},
	}
	spawned, err := b.SpawnWork(ctx, attempt.Attempt.ID, attempt.LeaseToken, spawnInput, "spawn-research-v1")
	if err != nil {
		t.Fatalf("SpawnWork: %v", err)
	}
	replayed, err := b.SpawnWork(ctx, attempt.Attempt.ID, attempt.LeaseToken, spawnInput, "spawn-research-v1")
	if err != nil || replayed.WorkItem.ID != spawned.WorkItem.ID {
		t.Fatalf("SpawnWork replay = %#v, err=%v", replayed, err)
	}
	changed := spawnInput
	changed.Title = "다른 작업"
	if _, err := b.SpawnWork(ctx, attempt.Attempt.ID, attempt.LeaseToken, changed, "spawn-research-v1"); !errors.Is(err, ErrWorkActionConflict) {
		t.Fatalf("changed SpawnWork replay error = %v, want %v", err, ErrWorkActionConflict)
	}
	if spawned.Edge == nil || spawned.Edge.FromItemID != spawned.WorkItem.ID || spawned.Edge.ToItemID != parent.ID || spawned.Edge.EdgeType != "blocks" {
		t.Fatalf("spawned dependency edge = %#v", spawned.Edge)
	}

	attachment, err := b.AttachWorkResource(ctx, spawned.WorkItem.ID, WorkResourceInput{
		ResourceKey: "jira:APP-12", Kind: "ticket", Source: "jira", Authority: "external",
		Title: "APP-12 사람 검토", URI: "https://jira.example.test/browse/APP-12",
		Summary: "사람이 관리하는 검토 상태", ExternalID: "APP-12", Revision: "7",
		Metadata: json.RawMessage(`{"status":"In Progress"}`), Role: "input",
	})
	if err != nil {
		t.Fatalf("AttachWorkResource: %v", err)
	}
	if attachment.Resource.Authority != "external" || attachment.Link.WorkItemID != spawned.WorkItem.ID {
		t.Fatalf("resource attachment = %#v", attachment)
	}
	updated, err := b.AttachWorkResource(ctx, spawned.WorkItem.ID, WorkResourceInput{
		ResourceKey: "jira:APP-12", Kind: "ticket", Source: "jira", Authority: "external",
		Title: "APP-12 사람 검토", URI: "https://jira.example.test/browse/APP-12",
		Summary: "사람이 관리하는 검토 완료", ExternalID: "APP-12", Revision: "8",
		Metadata: json.RawMessage(`{"status":"Done"}`), Role: "input",
	})
	if err != nil || updated.Resource.ID != attachment.Resource.ID || updated.Link.ID != attachment.Link.ID {
		t.Fatalf("resource replay update = %#v, err=%v", updated, err)
	}
	goalMap, err := b.GetGoalMap(ctx, namespaceID, true)
	if err != nil {
		t.Fatalf("GetGoalMap: %v", err)
	}
	if goalMap.ResourceTotal != 1 || len(goalMap.Resources) != 1 || goalMap.Resources[0].ID != attachment.Resource.ID {
		t.Fatalf("goal map resources = total:%d resources:%#v", goalMap.ResourceTotal, goalMap.Resources)
	}
	var sawParentCapabilities bool
	for _, item := range goalMap.WorkItems {
		if item.ID == parent.ID && strings.Join(item.RequiredCapabilities, ",") == "code" {
			sawParentCapabilities = true
		}
	}
	if !sawParentCapabilities {
		t.Fatalf("goal map did not expose parent routing capabilities: %#v", goalMap.WorkItems)
	}

	childResume, err := b.GetWorkResumeBundle(ctx, spawned.WorkItem.ID, 8)
	if err != nil {
		t.Fatalf("GetWorkResumeBundle child: %v", err)
	}
	if len(childResume.Resources) != 1 || childResume.Resources[0].Revision != "8" || childResume.Resources[0].Summary != "사람이 관리하는 검토 완료" {
		t.Fatalf("child resources = %#v", childResume.Resources)
	}
	parentResume, err := b.GetWorkResumeBundle(ctx, parent.ID, 8)
	if err != nil {
		t.Fatalf("GetWorkResumeBundle parent: %v", err)
	}
	if len(parentResume.Blockers) != 1 || parentResume.Blockers[0].ID != spawned.WorkItem.ID {
		t.Fatalf("parent blockers = %#v", parentResume.Blockers)
	}
	parentEvidence := submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{parentPreparation.CompletionConditions[0].ID}, "부모 조건을 확인했다", "evidence-parent-before-blocker")
	if _, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, parentPreparation.CompletionConditions[0].ID, "passed", []int64{parentEvidence.ID}, "", "verify-parent-before-blocker"); err != nil {
		t.Fatalf("VerifyWorkCondition parent: %v", err)
	}
	if _, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkFinishInput{
		Summary: "부모 완료", Result: "선행 작업을 건너뜀",
	}, "finish-parent-too-early"); !errors.Is(err, ErrWorkBlockersUnfinished) {
		t.Fatalf("early parent finish error = %v, want %v", err, ErrWorkBlockersUnfinished)
	}

	childAttempt := startWorkExecutionAttempt(t, b, ctx, spawned.WorkItem.ID, "agent-research")
	childConditionID := spawned.Preparation.CompletionConditions[0].ID
	childEvidence := submitWorkExecutionEvidence(t, b, ctx, childAttempt, []int64{childConditionID}, "선행 조사 결과를 확인했다", "evidence-child")
	if _, err := b.VerifyWorkCondition(ctx, childAttempt.Attempt.ID, childAttempt.LeaseToken, childConditionID, "passed", []int64{childEvidence.ID}, "", "verify-child"); err != nil {
		t.Fatalf("VerifyWorkCondition child: %v", err)
	}
	if _, err := b.FinishWorkAttempt(ctx, childAttempt.Attempt.ID, childAttempt.LeaseToken, WorkFinishInput{
		Summary: "공식 자료 조사를 마쳤다", Result: "필요한 제한과 출처를 확인했다",
	}, "finish-child"); err != nil {
		t.Fatalf("FinishWorkAttempt child: %v", err)
	}
	parentResume, err = b.GetWorkResumeBundle(ctx, parent.ID, 8)
	if err != nil {
		t.Fatalf("GetWorkResumeBundle parent after dependency: %v", err)
	}
	if len(parentResume.Blockers) != 0 || len(parentResume.DependencyResults) != 1 || parentResume.DependencyResults[0].WorkItem.ID != spawned.WorkItem.ID || parentResume.DependencyResults[0].Result != "필요한 제한과 출처를 확인했다" {
		t.Fatalf("parent dependency results = blockers:%#v results:%#v", parentResume.Blockers, parentResume.DependencyResults)
	}
	if _, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkFinishInput{
		Summary: "A-1 구현을 마쳤다", Result: "부모 조건과 선행 작업 결과를 모두 확인했다",
	}, "finish-parent-after-child"); err != nil {
		t.Fatalf("FinishWorkAttempt parent: %v", err)
	}
}
