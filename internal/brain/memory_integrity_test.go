package brain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAutomaticMemoryProvenanceAndComponentProgressPostgres(t *testing.T) {
	b, ctx, ns := newWorkExecutionTestBrain(t)
	component, err := b.CreateWorkPlanComponent(ctx, ns, WorkPlanComponentInput{Title: "작업 결과를 연결한다", Description: "결과와 출처를 찾을 수 있다", OwnedPaths: []string{"internal/brain"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := b.CreateWorkPlanTask(ctx, ns, WorkPlanTaskInput{ComponentID: component.ID, Title: "결과를 저장한다", Description: "다시 읽어 확인한다"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := b.PrepareWork(ctx, task.ID, "저장 후 확인", []CompletionConditionInput{{Kind: "test", Description: "저장된 결과를 읽는다", Required: true, Verification: json.RawMessage(`{"command":"read result"}`)}}, "prepare-integrity")
	if err != nil {
		t.Fatal(err)
	}
	lease := startWorkExecutionAttempt(t, b, ctx, task.ID, "integrity-agent")
	evidence := submitWorkExecutionEvidence(t, b, ctx, lease, []int64{prepared.CompletionConditions[0].ID}, "결과 확인", "evidence-integrity")
	if _, err = b.VerifyWorkCondition(ctx, lease.Attempt.ID, lease.LeaseToken, prepared.CompletionConditions[0].ID, "passed", []int64{evidence.ID}, "", "verify-integrity"); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit remember_work and link_work_item_memory.
	finish := WorkFinishInput{Summary: "저장 확인", Result: "결과를 다시 읽었다"}
	for i := 0; i < 2; i++ {
		if _, err = b.FinishWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, finish, "finish-integrity"); err != nil {
			t.Fatal(err)
		}
	}
	links, err := b.ListWorkItemMemoryLinks(ctx, task.ID)
	if err != nil || len(links) != 1 || links[0].MemoryType != "episode" {
		t.Fatalf("automatic result: %+v, %v", links, err)
	}
	episodeID := links[0].MemoryID
	if original, err := b.GetEpisode(ctx, episodeID); err != nil || original.Content == "" {
		t.Fatalf("unindexed episode detail: %+v %v", original, err)
	}
	var factID int64
	if err = b.pool.QueryRow(ctx, `INSERT INTO facts(namespace_id,content,confidence) VALUES($1,$2,0.9) RETURNING id`, ns, strings.Repeat("출처가 있는 결과. ", 80)).Scan(&factID); err != nil {
		t.Fatal(err)
	}
	if _, err = b.pool.Exec(ctx, `INSERT INTO fact_sources(fact_id,episode_id) VALUES($1,$2)`, factID, episodeID); err != nil {
		t.Fatal(err)
	}
	if fact, err := b.GetFact(ctx, factID); err != nil || fact.ID != factID {
		t.Fatalf("unindexed fact detail: %+v %v", fact, err)
	}
	links, err = b.ListWorkItemMemoryLinks(ctx, task.ID)
	if err != nil || len(links) != 2 {
		t.Fatalf("derived links: %+v, %v", links, err)
	}
	bundle, err := b.GetWorkResumeBundle(ctx, task.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, memory := range bundle.MemoryLinks {
		if memory.MemoryType == "fact" && memory.MemoryID == factID && memory.Derived {
			found = true
		}
	}
	if !found || bundle.Totals.MemoryLinks != 2 {
		t.Fatalf("agent resume lacks derived fact: %+v", bundle.MemoryLinks)
	}
	projection, err := b.GetGoalMap(ctx, ns, true)
	if err != nil {
		t.Fatal(err)
	}
	if projection.GoalTree.RootGoalID != nil || len(projection.Memories) != 2 || len(projection.UnassignedWork) != 2 {
		t.Fatalf("rootless map: %+v", projection)
	}
	found = false
	for _, edge := range projection.Edges {
		if edge.From == fmt.Sprintf("memory:fact:%d", factID) && edge.To == fmt.Sprintf("memory:episode:%d", episodeID) && edge.Relation == "derived_from" {
			found = true
		}
	}
	if !found {
		t.Fatal("original episode connection is missing")
	}
	plan, err := b.GetWorkPlan(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	progress := plan.Components[0].ExecutionProgress
	if progress == nil || progress.Status != "done" || progress.Done != 1 {
		t.Fatalf("derived completion: %+v", progress)
	}
	if _, err = b.CreateWorkPlanTask(ctx, ns, WorkPlanTaskInput{ComponentID: component.ID, Title: "추가 결과를 확인한다", Description: "추가 확인"}); err != nil {
		t.Fatal(err)
	}
	plan, err = b.GetWorkPlan(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	progress = plan.Components[0].ExecutionProgress
	if progress.Status != "doing" || progress.Total != 2 || progress.Done != 1 {
		t.Fatalf("progress after new work: %+v", progress)
	}
	// A deliberate link takes precedence and does not duplicate the derived row.
	if _, err = b.LinkWorkItemMemory(ctx, task.ID, "fact", factID, "decision"); err != nil {
		t.Fatal(err)
	}
	links, err = b.ListWorkItemMemoryLinks(ctx, task.ID)
	if err != nil || len(links) != 2 {
		t.Fatalf("manual precedence: %+v %v", links, err)
	}
	for _, link := range links {
		if link.MemoryType == "fact" && (link.Derived || link.Relation != "decision") {
			t.Fatalf("manual link overwritten: %+v", link)
		}
	}
	if _, err = b.pool.Exec(ctx, `UPDATE facts SET valid_until=now() WHERE id=$1`, factID); err != nil {
		t.Fatal(err)
	}
	bundle, err = b.GetWorkResumeBundle(ctx, task.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, memory := range bundle.MemoryLinks {
		if memory.MemoryType == "fact" {
			t.Fatal("expired fact reached agent resume")
		}
	}
}

func TestTicketlessMemoryAndDifferentWorkRolesPostgres(t *testing.T) {
	b, ctx, ns := newWorkExecutionTestBrain(t)
	b.embedder = &queueTestEmbedder{}
	var slug string
	if err := b.pool.QueryRow(ctx, `SELECT slug FROM namespaces WHERE id=$1`, ns).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	memory, err := b.QueueRemember(ctx, slug, "티켓 없이 남긴 검토 기준 100%", nil)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := b.CreateGoal(ctx, ns, "별도 목표", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.LinkGoalMemory(ctx, goal.ID, "episode", memory.ID, "constraint"); err != nil {
		t.Fatal(err)
	}
	items, err := b.ListMemory(ctx, ns, "episode", "100%", "", Pagination{Limit: 100})
	if err != nil || len(items) != 1 || items[0].MemoryID != memory.ID {
		t.Fatalf("ticketless memory: %+v %v", items, err)
	}
	projection, err := b.GetGoalMap(ctx, ns, true)
	if err != nil || len(projection.GoalTree.Goals) != 1 || len(projection.Memories) != 1 || len(projection.WorkItems)+len(projection.UnassignedWork) != 0 {
		t.Fatalf("ticketless goal: %+v %v", projection, err)
	}
	var count int
	if err = b.pool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE namespace_id=$1`, ns).Scan(&count); err != nil || count != 0 {
		t.Fatalf("memory created a ticket: %d %v", count, err)
	}
	orphan, err := b.QueueRemember(ctx, slug, "목표와 티켓 없이 남긴 원본", nil)
	if err != nil {
		t.Fatal(err)
	}
	var standaloneFact int64
	if err = b.pool.QueryRow(ctx, `INSERT INTO facts(namespace_id,content,confidence) VALUES($1,'독립된 사실',0.9) RETURNING id`, ns).Scan(&standaloneFact); err != nil {
		t.Fatal(err)
	}
	if _, err = b.pool.Exec(ctx, `INSERT INTO fact_sources(fact_id,episode_id) VALUES($1,$2)`, standaloneFact, orphan.ID); err != nil {
		t.Fatal(err)
	}
	context, err := b.MemoryContext(ctx, ns, "fact", standaloneFact)
	if err != nil || len(context.Memories) != 2 || len(context.Edges) != 1 || context.Edges[0].Relation != "derived_from" {
		t.Fatalf("standalone sources: %+v %v", context, err)
	}
	if _, err = b.MemoryContext(ctx, ns+999999, "fact", standaloneFact); err == nil {
		t.Fatal("cross-namespace source access accepted")
	}
	facts, err := b.QueryFacts(ctx, []string{slug}, nil, nil, Pagination{Limit: 100})
	if err != nil || len(facts) != 1 {
		t.Fatalf("unindexed fact list: %+v %v", facts, err)
	}
	for _, role := range []string{"implement", "review"} {
		work, err := b.CreateWorkItemWithCapabilities(ctx, ns, WorkItemInput{Title: role, Description: role + " only", Status: "ready", Owner: role}, []string{role})
		if err != nil {
			t.Fatal(err)
		}
		record, err := b.QueueRemember(ctx, slug, role+" 결과", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.LinkWorkItemMemory(ctx, work.ID, "episode", record.ID, "context"); err != nil {
			t.Fatal(err)
		}
		var factID int64
		if err = b.pool.QueryRow(ctx, `INSERT INTO facts(namespace_id,content,confidence) VALUES($1,$2,0.9) RETURNING id`, ns, role+" 사실").Scan(&factID); err != nil {
			t.Fatal(err)
		}
		if _, err = b.pool.Exec(ctx, `INSERT INTO fact_sources(fact_id,episode_id) VALUES($1,$2)`, factID, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	works, err := b.ListWorkItems(ctx, []string{slug}, "", nil, Pagination{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, work := range works {
		bundle, err := b.GetWorkResumeBundle(ctx, work.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(bundle.WorkItem.RequiredCapabilities) != 1 || bundle.WorkItem.RequiredCapabilities[0] != work.Owner {
			t.Fatalf("role changed: %+v", bundle.WorkItem.RequiredCapabilities)
		}
		if len(bundle.MemoryLinks) != 2 {
			t.Fatalf("wrong context size: %+v", bundle.MemoryLinks)
		}
		for _, linked := range bundle.MemoryLinks {
			if !strings.Contains(linked.Content, work.Owner) {
				t.Fatalf("another role's memory leaked into %s: %+v", work.Owner, linked)
			}
		}
	}
	if _, err = b.ListMemory(ctx, ns, "invalid", "", "", Pagination{Limit: 100}); err == nil {
		t.Fatal("invalid memory type accepted")
	}
	if items, err = b.ListMemory(ctx, ns+999999, "", "", "", Pagination{Limit: 100}); err != nil || len(items) != 0 {
		t.Fatalf("namespace isolation: %+v %v", items, err)
	}
}
