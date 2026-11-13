<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

# Structure, layering and naming

Read this before adding a package, a resource or a dependency between layers, and when a name is unclear.

## Directory layout

```
api/<domain>/<version>/   proto sources and generated code: the external contract
cmd/<app>/                entry point main.go and its hand-written di.go
configs/                  runtime configuration, no secrets
internal/conf/            service config proto (app.proto) and generated code
internal/server/          builds servers, registers handlers, consumers and tasks
internal/service/         transport adapters, one file per resource
internal/biz/             domain models, use cases, repo interfaces, errors
internal/data/            repo implementations, storage clients, gRPC clients
internal/i18n/            language bundles and translations
migrations/<database>/    SQL scripts applied by hand: 0_schema.sql, 1_baseline.sql, incremental scripts
third_party/              imported .proto copied by make proto from the versions in go.mod
deploy/nginx/             nginx config in front of the service; subdirectories mirror /etc/nginx/
```

`biz`, `data`, `service` and `i18n` are shared by every entry under `cmd/`; each entry's `di.go` wires only what it uses.

## Layering

```
   client ──► DTO ──► service ──► DO ──► biz ──► DO ──► data ──► PO ──► storage
                                  ▲                ▲
                                  │ declares       │ implements
                                  └─ repo interface┘

   DTO  proto request / response
   DO   domain object: no proto, no storage tags
   PO   GORM model, private to data
```

| Layer | Owns | Speaks at its boundary | Never touches |
|---|---|---|---|
| service | — | DTO ↔ DO | PO, storage clients, business rules |
| biz | DO | DO | DTO, PO, storage clients |
| data | PO | DO ↔ PO | DTO, business rules |

- `biz` does not import `api/...`; only the `di.go` files wire layers together.
- Exception: Keel's messaging and task interfaces. `biz` may depend on `knats.Publisher`, `kasynq.Enqueuer`,
  `kasynq.TaskType` / `knats.EventType` and Keel's sentinel errors (`kasynq.ErrTaskIDConflict`,
  `kasynq.ErrDuplicateTask`) without wrapping them in a port (`GLOSSARY.md`, "Port"). Concrete infrastructure handles
  (`*kdb.DB`, `*kredis.Redis`) still stay out of `biz`.

A dependency in the wrong direction means the design changes; do not add the import.

## Layer responsibilities

- **service**: proto ↔ DO conversion, boundary validation, applying FieldMasks, returning biz errors; embeds
  `Unsafe<Resource>ServiceServer` (a missing method fails to compile). List requests pass `offset`/`limit` through untouched.
- **biz**: DOs, use cases, repo interfaces, typed errors, `ListOptions` that do not leak storage primitives; fills the
  default page size when `limit` is 0 (the maximum lives only in the proto `lte`).
- **data**: implements `biz.<Resource>Repo`; the constructor returns the interface. POs are unexported; write
  `TableName()` when the stored shape differs from the DO. Driver errors map to biz's typed errors, so callers never
  branch on the driver.
- **server**: builds servers and registers handlers, routes, consumers and tasks, nothing else.

## Naming and error reasons

- Resource names are PascalCase and singular (`Order`) in RPC and message names; collection RPCs are `List<Resource>`
  (`ListOrder(ListOrderRequest) returns (ListOrderReply)`), option lists `List<Resource>Options`. Replies end in
  `Reply`, not `Response`; Get/Create/Update return the resource message itself and deletes return
  `google.protobuf.Empty`, with no wrapper.
- Types: `<Resource>Repo`, `<Resource>UseCase`, `<Resource>Service`.
- POs: the type is the table name in lowerCamelCase without a `Model` suffix (`sys_user` → `sysUser`); data-layer
  variables are not named after tables. Conversions are the PO method `(m *<po>) toBiz()` and the function
  `new<Resource>(do)`, in the same file as the PO and its `TableName()`.
- Error reasons are declared in `api/<domain>/<version>/error_reason.proto` and appear in `biz` as
  `Err<Resource><Cause>`.
- Task contracts: `var Task<Name> = kasynq.NewTaskType[Payload]("<domain>:<action>")`, next to the use case that
  enqueues it; use `struct{}` for no payload ([naming](keel:docs/tasks.md#define-a-task)).
- Event contracts: one `var Stream<Domain> = knats.NewStream("<domain>")` per domain and
  `var Event<Name> = knats.NewEventType[Payload](Stream<Domain>, subject)`, next to the publisher in biz. `subject` is
  only the business topic; Keel adds the `<domain>.<stream>.` prefix ([Naming](keel:docs/messaging.md#naming)).

## Types and models

Amounts use `decimal.Decimal`. Status fields use typed enums (`type XxxStatus string` plus constants). Every repo or
port implementation has a compile-time assertion `var _ biz.XxxRepo = (*impl)(nil)`.
