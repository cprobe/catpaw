# Redis Sentinel Plugin Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a new `redis_sentinel` plugin that monitors Redis Sentinel targets, emits node and per-master events, and exposes LLM-friendly diagnosis tools.

**Architecture:** Reuse the existing `plugins/redis` transport and remote-plugin patterns, but keep Sentinel configuration and semantics separate. Implement in layers: plugin registration and types, config normalization and validation, RESP accessor and parsers, lightweight gather checks, diagnosis tools, then fake-server tests.

**Tech Stack:** Go, catpaw plugin framework, diagnose registry, RESP over TCP/TLS, existing Redis fake server test style

---

### Task 1: Register the plugin and define core types

**Files:**
- Create: `plugins/redis_sentinel/sentinel.go`
- Create: `plugins/redis_sentinel/types.go`
- Modify: `agent/agent.go`

**Step 1:** Add `pluginName = "redis_sentinel"` and `init()` registration.

**Step 2:** Define `RedisSentinelPlugin`, `Instance`, `Partial`, `MasterRef`, and check config structs.

**Step 3:** Add the blank import in `agent/agent.go`.

**Step 4:** Run targeted compile checks with `go test ./plugins/redis_sentinel ./agent`.

### Task 2: Implement config normalization and validation

**Files:**
- Create: `plugins/redis_sentinel/config.go`

**Step 1:** Implement `ApplyPartials()` with Sentinel-specific fields only.

**Step 2:** Implement `Init()` defaults for timeout, read timeout, concurrency, target normalization, and check defaults.

**Step 3:** Validate unique targets, unique master names, event status fields, and threshold relationships.

**Step 4:** Add tests in `plugins/redis_sentinel/sentinel_test.go` for normalization and validation.

### Task 3: Implement accessor and Sentinel reply parsing

**Files:**
- Create: `plugins/redis_sentinel/accessor.go`
- Create: `plugins/redis_sentinel/parse.go`

**Step 1:** Reuse Redis RESP client code pattern without `SELECT`.

**Step 2:** Add helpers for `PING`, `ROLE`, `INFO`, `SENTINEL MASTERS`, `SENTINEL MASTER`, `SENTINEL REPLICAS`, `SENTINEL SENTINELS`, `SENTINEL CKQUORUM`, and `SENTINEL GET-MASTER-ADDR-BY-NAME`.

**Step 3:** Parse alternating key-value arrays into structured maps and normalize common fields.

**Step 4:** Add accessor/parser tests backed by a fake Sentinel server.

### Task 4: Implement gather path and event generation

**Files:**
- Create: `plugins/redis_sentinel/gather.go`
- Create: `plugins/redis_sentinel/checks.go`

**Step 1:** Reuse Redis gather concurrency and hung-target pattern.

**Step 2:** Implement node-level checks: connectivity, role, masters overview.

**Step 3:** Implement effective master set resolution.

**Step 4:** Implement per-master checks: ckquorum, sdown, odown, master_addr_resolution.

**Step 5:** Implement optional checks: peer_count, known_replicas, known_sentinels, failover_in_progress, tilt.

**Step 6:** Add gather tests for healthy, missing-master, quorum-fail, and down-state paths.

### Task 5: Implement diagnosis tools and pre-collector

**Files:**
- Create: `plugins/redis_sentinel/diagnose.go`
- Create: `plugins/redis_sentinel/diagnose_test.go`

**Step 1:** Register category, accessor factory, and pre-collector.

**Step 2:** Implement task-shaped tools:
- `sentinel_overview`
- `sentinel_master_health`
- `sentinel_replicas`
- `sentinel_sentinels`
- `sentinel_info`

**Step 3:** Add diagnose hints aligned with the new tool granularity.

**Step 4:** Add registry and tool behavior tests.

### Task 6: Build fake Sentinel server test asset

**Files:**
- Create: `plugins/redis_sentinel/sentinel_test.go`

**Step 1:** Implement a fake RESP Sentinel server similar to `plugins/redis/redis_test.go`.

**Step 2:** Support `AUTH`, `PING`, `ROLE`, `INFO`, and required `SENTINEL` subcommands.

**Step 3:** Add mutable config so tests can inject state transitions.

### Task 7: Verify and wire everything together

**Files:**
- Modify: `plugins/redis_sentinel/design.md` only if implementation reveals a real mismatch

**Step 1:** Run targeted tests for `plugins/redis_sentinel`.

**Step 2:** Run a broader compile/test sweep that includes `agent` and diagnose registry paths.

**Step 3:** Fix any API mismatches and keep implementation aligned with the final docs.
