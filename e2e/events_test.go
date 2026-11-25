//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/go-keel/keel-layout/e2e/harness"
	"github.com/go-keel/keel/core/kasynq"
	"github.com/go-keel/keel/core/knats"
)

// index returns the position in lines of the first line with msg and fields, or -1.
func index(lines []harness.Line, msg string, fields map[string]any) int {
	for i, l := range lines {
		if l.Match(msg, fields) {
			return i
		}
	}
	return -1
}

// Basis: README.md, the request flow (HTTP → event → task); internal/service/consumer.go and task.go.
func testEventToTask(t *testing.T, e *env) {
	mark := e.worker.Mark()
	title := uniq("secret title")
	id := e.create(t, title)
	e.worker.WaitLogSince(t, mark, timeout, "sending todo reminder", map[string]any{"todo_id": id})
	ls := e.worker.LogsSince(mark)
	f := map[string]any{"todo_id": id}
	enq, handled, sent := index(ls, "reminder task enqueued", f), index(ls, "todo_created event handled", f), index(ls, "sending todo reminder", f)
	if enq < 0 || handled < enq || sent < 0 {
		t.Fatalf("order: enqueued %d, handled %d, sending %d", enq, handled, sent)
	}
	if strings.Contains(e.worker.Output(), title) {
		t.Fatal("the worker log carries the title")
	}
}

// Basis: internal/biz/todo.go, EnqueueReminder (TaskID and Retention deduplicate); keel:docs/tasks.md, "Enqueue options".
func testDuplicateEvent(t *testing.T, e *env) {
	id := e.create(t, uniq("dup"))
	e.worker.WaitLog(t, "sending todo reminder", map[string]any{"todo_id": id})
	if _, err := e.js.Publish(context.Background(), todoSubject, []byte(`{"id":`+id+`,"title":"dup"}`)); err != nil {
		t.Fatal(err)
	}
	e.worker.WaitLog(t, "reminder task already enqueued", map[string]any{"todo_id": id})
	if n := countLines(e.worker.LogsSince(0), "sending todo reminder", map[string]any{"todo_id": id}); n != 1 {
		t.Fatalf("%d reminders sent for one todo", n)
	}
}

func countLines(lines []harness.Line, msg string, fields map[string]any) int {
	n := 0
	for _, l := range lines {
		if l.Match(msg, fields) {
			n++
		}
	}
	return n
}

// waitGaveUp waits for the worker to give up on a message and returns the reason it logged.
func (e *env) waitGaveUp(t *testing.T, mark int) string {
	t.Helper()
	l := e.worker.WaitLogSince(t, mark, timeout, "nats consumer gave up on message", map[string]any{"durable": todoDurable, "level": "ERROR"})
	return fmt.Sprint(l["reason"])
}

// waitDeadLetters waits until DEAD_LETTER holds n advisories and returns them.
func (e *env) waitDeadLetters(t *testing.T, n int) []knats.DeadLetter {
	t.Helper()
	var dls []knats.DeadLetter
	waitFor(t, fmt.Sprintf("%d dead letters", n), func() bool {
		var err error
		dls, err = knats.ListDeadLetters(context.Background(), e.js)
		return err == nil && len(dls) >= n
	})
	return dls
}

// Basis: keel:docs/messaging.md, "Handler results, retries and dead letters", "Event contracts" and "Dead letters:
// advisories, not copies".
func testDeadLetters(t *testing.T, e *env) {
	ctx := context.Background()
	before, err := knats.ListDeadLetters(ctx, e.js)
	if err != nil {
		t.Fatal(err)
	}
	mark := e.worker.Mark()
	if _, err := e.js.Publish(ctx, todoSubject, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if r := e.waitGaveUp(t, mark); !strings.Contains(r, "invalid todo_created event: todo_id 0") {
		t.Fatalf("reason %q", r)
	}
	dls := e.waitDeadLetters(t, len(before)+1)
	invalid := dls[len(dls)-1]
	if invalid.Consumer != todoDurable || invalid.Deliveries != 1 || !strings.Contains(invalid.Reason, "invalid todo_created event: todo_id 0") {
		t.Fatalf("dead letter %+v", invalid)
	}

	mark = e.worker.Mark()
	if _, err := e.js.Publish(ctx, todoSubject, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	e.waitGaveUp(t, mark)
	e.waitDeadLetters(t, len(before)+2)

	mark = e.worker.Mark()
	if err := knats.ReplayDeadLetter(ctx, e.js, invalid.Seq); err != nil {
		t.Fatal(err)
	}
	if r := e.waitGaveUp(t, mark); !strings.Contains(r, "invalid todo_created event: todo_id 0") {
		t.Fatalf("replayed reason %q", r)
	}
	e.waitDeadLetters(t, len(before)+3)
}

// Basis: keel:docs/tasks.md, "Handle" (a payload that cannot be decoded is archived, not retried).
func testBadTaskArchived(t *testing.T, e *env) {
	ctx := context.Background()
	id := uniq("bad-task")
	if _, err := kasynq.NewTaskClient(e.rdb).Enqueue(ctx, "todo:send_reminder", []byte("not json"), kasynq.MaxRetry(3), kasynq.TaskID(id)); err != nil {
		t.Fatal(err)
	}
	in := asynq.NewInspectorFromRedisClient(e.rdb)
	waitFor(t, "the bad task archived", func() bool {
		info, err := in.GetTaskInfo("default", id)
		return err == nil && info.State == asynq.TaskStateArchived
	})
	info, _ := in.GetTaskInfo("default", id)
	if info.Retried != 0 {
		t.Fatalf("retried %d times, want 0", info.Retried)
	}
}

// Basis: keel:docs/tasks.md, "Cron"; internal/server/scheduler.go, internal/service/task.go.
func testCronRegisteredAndDailyReport(t *testing.T, e *env) {
	in := asynq.NewInspectorFromRedisClient(e.rdb)
	// The scheduler writes its entries on asynq's 10 s heartbeat, so this waits longer than the default.
	waitWithin(t, 25*time.Second, "the cron entry registered", func() bool {
		entries, err := in.SchedulerEntries()
		if err != nil {
			return false
		}
		for _, en := range entries {
			if en.Spec == "0 8 * * *" && en.Task.Type() == "todo:daily_report" {
				return true
			}
		}
		return false
	})
	mark := e.worker.Mark()
	if _, err := kasynq.NewTaskClient(e.rdb).Enqueue(context.Background(), "todo:daily_report", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	e.worker.WaitLogSince(t, mark, timeout, "executing daily report task", nil)
}

// leaseHolder returns the value of the cron lease, "" when nobody holds it.
func (e *env) leaseHolder(t *testing.T) string {
	t.Helper()
	v, err := e.rdb.Get(context.Background(), cronLeaseKey).Result()
	if errors.Is(err, redis.Nil) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Basis: keel:docs/tasks.md, "One replica schedules".
func testCronLeaseOneHolder(t *testing.T, e *env) {
	l := e.worker.WaitLog(t, "cron scheduler holds the lease; this instance enqueues cron tasks", map[string]any{"key": cronLeaseKey})
	holder := fmt.Sprint(l["instance_id"])
	if got := e.leaseHolder(t); got != holder {
		t.Fatalf("lease value %q, the worker logged %q", got, holder)
	}
	second := e.startWorker(t, t, harness.Workdir(t))
	// A standby's scheduler waits for the lease and logs nothing; the holder's lease stays put.
	if second.FindSince(0, "cron scheduler holds the lease; this instance enqueues cron tasks", nil) != nil {
		t.Fatal("the second worker also holds the lease")
	}
	if got := e.leaseHolder(t); got != holder {
		t.Fatalf("lease moved to %q with the holder still running", got)
	}
	second.Stop(t)
	if got := e.leaseHolder(t); got != holder {
		t.Fatalf("stopping the standby changed the lease to %q", got)
	}
}

// Basis: keel:docs/tasks.md, "One replica schedules" (a clean stop releases the lease); keel:docs/messaging.md, "New
// durables start from new messages" (a restarted durable resumes where it stopped).
func testWorkerStopReleasesLeaseAndResumes(t *testing.T, e *env) {
	if e.leaseHolder(t) == "" {
		t.Fatal("no lease before the stop")
	}
	e.worker.Stop(t)
	if v := e.leaseHolder(t); v != "" {
		t.Fatalf("lease still %q right after a clean stop", v)
	}
	id := e.create(t, uniq("while stopped"))
	e.worker = e.startWorker(e.rt, t, harness.Workdir(t))
	e.worker.WaitLog(t, "sending todo reminder", map[string]any{"todo_id": id})
	e.worker.WaitLog(t, "cron scheduler holds the lease; this instance enqueues cron tasks", nil)
}

// Basis: internal/biz/todo.go, CreateTodo (a failed publish does not undo the create); keel:docs/messaging.md, "A
// consumer that dies takes the server down".
func testPublishFailureAndLostConsumer(t *testing.T, e *env) {
	if err := e.js.DeleteStream(context.Background(), todoStream); err != nil {
		t.Fatal(err)
	}
	mark := e.api.Mark()
	title := uniq("unpublished")
	id := e.create(t, title)
	e.api.WaitLogSince(t, mark, timeout, "failed to publish todo_created event", map[string]any{"todo_id": id, "level": "ERROR"})
	if e.count(t, "id = ? AND title = ?", id, title) != 1 {
		t.Fatal("the create was rolled back")
	}
	e.call(t, http.MethodGet, "/v1/todos/"+id, "").want(t, 200)
	code, ok := e.worker.Wait(timeout)
	if !ok || code == 0 {
		t.Fatalf("worker after losing its durable: exited %v, code %d\n%s", ok, code, e.worker.Tail(20))
	}
}
