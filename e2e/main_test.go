//go:build e2e

// Package e2e runs the three production binaries (api, svc, worker) against real servers and tests them from outside,
// over HTTP, gRPC, NATS and Redis, once per round in rounds. docs/guides/testing.md, "End-to-end tests".
package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/e2e/harness"
	"github.com/go-keel/keel-layout/internal/testdb"
)

// round is one deployment the tests run against: the driver of each database. A repository edits rounds to match how
// it deploys (one database only, a different driver per database); run a single round with
// make e2e ARGS='-run /<name>'.
type round struct {
	name     string
	business string // database.default: migrations/business/<driver>/0_schema.sql
}

var rounds = []round{
	{name: "mysql", business: "mysql"},
	{name: "postgres", business: "postgres"},
}

// Example: delete after forking (through "Example end")
// Names from configs/config.yaml (app.domain, app.name) and the example code. A case of your own reads such values
// with harness.Config instead of spelling them out.
const (
	domain       = "keel"
	cronLeaseKey = domain + ":keel-layout:cron_scheduler"
	todoStream   = domain + "_todo"
	todoSubject  = domain + ".todo.created"
	todoDurable  = "todo_created"
)

// Example end

// timeout is the default wait for anything a test waits on.
const timeout = harness.WaitTimeout

// cases run in this order in every round, one after another; each makes its own uniquely named data and asserts only
// on that. The last ones break the round's shared processes and stay last.
var cases = []struct {
	name string
	fn   func(t *testing.T, e *env)
}{
	// Example: delete after forking (through "Example end")
	{"durable_starts_from_new_events", testDurableStartsFromNewEvents},
	{"http_create", testHTTPCreate},
	{"http_list", testHTTPList},
	{"http_list_filters_and_sort", testHTTPListFiltersAndSort},
	{"http_list_bad_query", testHTTPListBadQuery},
	{"http_update_mask", testHTTPUpdateMask},
	{"http_update_unchanged_value", testHTTPUpdateUnchangedValue},
	{"http_create_validation", testHTTPCreateValidation},
	{"http_get_delete", testHTTPGetDelete},
	{"grpc_crud", testGRPCCRUD},
	{"grpc_errors_and_locale", testGRPCErrorsAndLocale},
	{"grpc_keel_client", testGRPCKeelClient},
	{"grpc_health_and_reflection", testGRPCHealthAndReflection},
	{"grpc_request_log", testGRPCRequestLog},
	{"sse_watch", testSSEWatch},
	{"grpc_streams", testGRPCStreams},
	{"websocket_lookup", testWebSocketLookup},
	{"export_csv", testExportCSV},
	{"raw_sse", testRawSSE},
	{"raw_websocket", testRawWebSocket},
	{"raw_download", testRawDownload},
	{"webhook_and_hot_reload", testWebhookAndHotReload},
	{"signed_prefix_filter", testSignedPrefixFilter},
	{"raw_routes_not_logged", testRawRoutesNotLogged},
	{"error_body_and_locale", testErrorBodyAndLocale},
	{"database_error_hidden", testDatabaseErrorHidden},
	{"event_to_task", testEventToTask},
	{"duplicate_event", testDuplicateEvent},
	{"dead_letters", testDeadLetters},
	{"bad_task_archived", testBadTaskArchived},
	{"cron_registered_and_daily_report", testCronRegisteredAndDailyReport},
	{"cron_lease_one_holder", testCronLeaseOneHolder},
	{"worker_stop_releases_lease_and_resumes", testWorkerStopReleasesLeaseAndResumes},
	{"redis_lock", testRedisLock},
	{"redis_ratelimit", testRedisRatelimit},
	{"snowflake", testSnowflake},
	{"health_and_route_log", testHealthAndRouteLog},
	{"request_log", testRequestLog},
	{"stream_request_log", testStreamRequestLog},
	{"trace_follows_event_and_task", testTraceFollowsEventAndTask},
	{"auth_http", testAuthHTTP},
	{"auth_cookie", testAuthCookie},
	{"auth_rs256", testAuthRS256},
	{"auth_grpc", testAuthGRPC},
	{"client_ip_outside_chain", testClientIPOutsideChain},
	{"config_placeholder_and_env", testConfigPlaceholderAndEnv},
	{"config_dev_overlay_and_keel_env", testConfigDevOverlayAndKeelEnv},
	{"config_framework_not_reloaded", testConfigFrameworkNotReloaded},
	{"shutdown_second_signal", testShutdownSecondSignal},
	{"startup_errors", testStartupErrors},
	{"database_startup_errors", testDatabaseStartupErrors},
	{"server_startup_errors", testServerStartupErrors},
	{"publish_failure_and_lost_consumer", testPublishFailureAndLostConsumer},
	// Example end
}

// env is one round: its databases, the shared api, svc and worker, and clients for what the tests inspect.
type env struct {
	round    round
	rt       *testing.T // the round's test: owns the shared processes
	dsn      string     // database.default
	db       *gorm.DB
	http     string // base URL of api
	httpAddr string
	grpcAddr string
	api      *harness.Process
	svc      *harness.Process
	worker   *harness.Process
	todo     v1.TodoServiceClient // Example: delete after forking
	nc       *nats.Conn
	js       jetstream.JetStream
	rdb      *redis.Client
	preID    string // Example: delete after forking
}

func TestE2E(t *testing.T) {
	for _, v := range harness.Vars {
		harness.Env(t, v)
	}
	harness.Build(t, "api", "svc", "worker")
	for _, r := range rounds {
		t.Run(r.name, func(t *testing.T) {
			e := setup(t, r)
			for _, c := range cases {
				if !t.Run(c.name, func(t *testing.T) { c.fn(t, e) }) && e.brokenShared() {
					t.Fatalf("a shared process exited during %s; the rest of the round cannot run", c.name)
				}
			}
		})
	}
}

// setup starts a round from empty servers: fresh databases, Redis database 0 flushed, every NATS stream deleted; then
// svc, api and, while it has a server, the worker.
func setup(t *testing.T, r round) *env {
	e := &env{round: r, rt: t}
	e.dsn = testdb.Create(t, "business", r.business, fmt.Sprintf("e2e_%s_%d", r.name, time.Now().UnixNano()))
	e.db = openDB(t, r.business, e.dsn)

	harness.FlushRedis(t)
	e.rdb = redis.NewClient(&redis.Options{Addr: harness.Env(t, "TEST_REDIS_ADDR")})
	t.Cleanup(func() { _ = e.rdb.Close() })
	nc, err := nats.Connect(harness.Env(t, "TEST_NATS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	e.nc = nc
	e.js, err = jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name := range e.js.StreamNames(ctx).Name() {
		if err := e.js.DeleteStream(ctx, name); err != nil {
			t.Fatalf("delete NATS stream %s: %v", name, err)
		}
	}

	e.grpcAddr = harness.FreeAddr(t)
	e.svc = e.startSvc(t, harness.Workdir(t))
	e.httpAddr = harness.FreeAddr(t)
	e.http = "http://" + e.httpAddr
	e.api = e.startAPI(t, harness.Workdir(t), e.httpAddr)

	// Example: delete after forking (through "Example end")
	conn, err := grpc.NewClient(e.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	e.todo = v1.NewTodoServiceClient(conn)
	e.preID = e.create(t, uniq("before the worker")) // its event is published before the durable exists
	// Example end

	// The worker serves only the example's consumer, tasks and cron jobs; with them deleted it has no server and fails
	// startup with app.no-servers (README, "Delete the examples"). Start it again with your first consumer or task.
	e.worker = e.startWorker(t, t, harness.Workdir(t)) // Example: delete after forking
	return e
}

func openDB(t *testing.T, driver, dsn string) *gorm.DB {
	t.Helper()
	var d gorm.Dialector
	if driver == "mysql" {
		d = gormmysql.Open(dsn)
	} else {
		d = postgres.Open(dsn)
	}
	db, err := gorm.Open(d, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			_ = s.Close()
		}
	})
	return db
}

// processEnv is what every process of the round gets: test servers, JSON log, the round's database.
func (e *env) processEnv(t testing.TB) []string {
	return append(harness.ServerEnv(t), harness.DBEnv("default", e.round.business, e.dsn)...)
}

// startAPI starts an api in dir listening on addr, with extra variables, and waits until /health answers.
func (e *env) startAPI(t testing.TB, dir, addr string, extra ...string) *harness.Process {
	t.Helper()
	p := harness.Start(t, "api", dir, append(append(e.processEnv(t), "KEEL_HTTP_ADDR="+addr), extra...)...)
	p.WaitHTTP(t, "http://"+addr+"/health")
	return p
}

func (e *env) startSvc(t testing.TB, dir string, extra ...string) *harness.Process {
	t.Helper()
	p := harness.Start(t, "svc", dir, append(append(e.processEnv(t), "KEEL_GRPC_ADDR="+e.grpcAddr), extra...)...)
	p.WaitDial(t, e.grpcAddr)
	return p
}

// startWorker starts a worker owned by owner (stopped when owner ends; the round's test for a shared worker) and waits,
// failing t, until its task server runs and its durable consumers exist.
func (e *env) startWorker(owner, t testing.TB, dir string, extra ...string) *harness.Process {
	t.Helper()
	p := harness.Start(owner, "worker", dir, append(e.processEnv(t), extra...)...)
	p.WaitLog(t, "Starting processing", nil)
	// Example: delete after forking (through "Example end")
	deadline := time.Now().Add(harness.WaitTimeout)
	for {
		if _, err := e.js.Consumer(context.Background(), todoStream, todoDurable); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("durable %s on %s not created within %s: %v\n%s", todoDurable, todoStream, harness.WaitTimeout, err, p.Tail(50))
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Example end
	return p
}

// brokenShared reports whether a shared process has exited.
func (e *env) brokenShared() bool {
	return e.api.Exited() || e.svc.Exited() || e.worker != nil && e.worker.Exited()
}

var seq int

// uniq returns s with a suffix unique within the test run.
func uniq(s string) string {
	seq++
	return fmt.Sprintf("%s %d-%d", s, time.Now().UnixNano()%1e9, seq)
}
