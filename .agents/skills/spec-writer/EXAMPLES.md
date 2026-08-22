# spec-writer — example prompts

Copy-paste prompts for common scenarios. Each one is self-contained — paste it into the agent of your choice (Claude Code, Cursor, Windsurf, Copilot, Aider, Codex) once `SKILL.md` is loaded.

The examples are grouped by:

1. **By prompt level** — minimal, standard, complete
2. **By feature class** — backend, frontend, library, CLI, data, refactor
3. **By workflow** — decomposition, plan mode, retrofit, revising a draft, resolving open questions

---

## 1. By prompt level

The difference between levels is **what you've already decided vs. what's still open**, not length.

### Level 1 — Minimal

Use when the project has a thorough `AGENTS.md` / `CLAUDE.md`. The skill infers constraints from there.

```
Use the spec-writer skill to create a spec for
adding pagination to the customer list endpoint.
```

```
Use the spec-writer skill to create a spec for
soft-delete on the orders table.
```

```
Use the spec-writer skill to spec out
exporting the dashboard to PDF.
```

### Level 2 — Standard *(recommended default)*

Use when the main decisions are already made.

```
Use the spec-writer skill to create a spec for
rate limiting on the public API.

Context:
- Token bucket, per IP and per authenticated user (JWT sub claim)
- Redis already in stack, in-memory fallback when REDIS_URL is empty
- Apply only to /api/v1/*, not /health or /metrics
- 100 req/min unauthenticated, 600 req/min authenticated
- 429 response with Retry-After header

Output: specs/rate-limiting/SPEC.md
```

```
Use the spec-writer skill to create a spec for
a webhook delivery system.

Context:
- HMAC-SHA256 signed payloads, signature in X-Signature header
- Exponential backoff: 1s, 5s, 30s, 5m, 30m, 2h (six attempts)
- Persisted retries in Postgres (already used for other queues)
- Webhook subscribers stored per tenant
- Dead letter queue after the sixth failed attempt

Output: specs/webhook-delivery/SPEC.md
```

### Level 3 — Complete

Use when there are real open decisions or the scope is ambiguous. The agent will mark blocking unresolved points `[NEEDS CLARIFICATION]` — and log the non-blocking ones as assumptions with a chosen default you can veto — rather than silently invent answers.

```
Use the spec-writer skill to create a spec for
multi-tenant data isolation in the analytics service.

Decided:
- One Postgres database, isolation via schema-per-tenant
- Tenant ID resolved from JWT claim, propagated via context
- Migration: existing single-tenant data moves into a "default" tenant

Still undecided — surface trade-offs in the spec:
- Connection pooling: one pool per schema vs one pool with SET search_path per query
- How tenant context propagates into the existing async worker pool

What this is NOT:
- Not row-level security with policies (rejected: complexity in joins)
- Not separate databases (rejected: backup/maintenance overhead)

Output: specs/multitenant-isolation/SPEC.md
```

```
Use the spec-writer skill to create a spec for
real-time notifications in the web app.

Decided:
- Server-side: existing Node.js API
- Client-side: existing React SPA
- Notifications are user-scoped (no broadcast)

Still undecided — surface trade-offs:
- Transport: WebSocket vs Server-Sent Events vs long polling
- Delivery guarantee: at-least-once vs at-most-once
- How offline users catch up on missed notifications

What this is NOT:
- Not push notifications (mobile, FCM/APNs) — separate spec
- Not email or SMS fallback — separate spec

Output: specs/realtime-notifications/SPEC.md
```

---

## 2. By feature class

The skill detects feature class in Phase 1 and chooses which sections of the template to include. These prompts show the breadth.

### Backend service

```
Use the spec-writer skill to create a spec for
an idempotency-key middleware for the payments API.

Context:
- POST /v1/payments must accept Idempotency-Key header
- Replays within 24h return the original response
- Storage: Redis (already in stack), 24h TTL
- If Redis is unavailable: reject the request (fail-loud, payments are critical)
- Concurrent requests with the same key: second one waits or returns 409 — surface trade-off

Output: specs/idempotency-key/SPEC.md
```

### Frontend / UI feature

```
Use the spec-writer skill to create a spec for
a command palette (Cmd+K) for the admin app.

Context:
- React, existing app uses Tailwind + Radix primitives
- Indexes: routes, customer search, recent actions, settings
- Keyboard-only navigation must work end-to-end
- Empty state, loading state, error state required
- Telemetry: track which result was selected, position in list

Out of scope:
- Not a global search across customer-facing app
- Not AI-powered suggestions (future)

Output: specs/command-palette/SPEC.md
```

### Library / SDK

```
Use the spec-writer skill to create a spec for
adding retry-with-backoff to the official SDK.

Context:
- SDK is published as @acme/api-client (TypeScript) and acme-api-client (Python)
- Default: retry on 429, 502, 503, 504; up to 3 retries; exponential backoff with jitter
- Configurable via constructor option { retry: { ... } }
- Must not retry mutations without an Idempotency-Key
- Semver: MINOR bump, fully backward compatible

Out of scope:
- Not adding circuit breaker (separate spec)
- Not changing default timeouts

Output: specs/sdk-retry-backoff/SPEC.md
```

### CLI / script

```
Use the spec-writer skill to create a spec for
a `db:migrate` command in our internal CLI.

Context:
- CLI is Go, uses spf13/cobra (existing pattern)
- Wraps golang-migrate with project conventions (config from env, structured logs)
- Subcommands: up, down, status, create
- Exit codes: 0 ok, 1 generic failure, 2 dirty migration state, 3 lock contention
- Must be safe to run in CI (no interactive prompts unless --interactive)

Output: specs/cli-db-migrate/SPEC.md
```

### Data / pipeline / ML

```
Use the spec-writer skill to create a spec for
a daily aggregation job that materializes hourly metrics into a daily table.

Context:
- Source: events_hourly (partitioned by hour, ~50M rows/day)
- Target: events_daily
- Idempotent: re-running for the same date overwrites, never duplicates
- Backfill plan needed for the last 90 days
- Runs on existing Airflow cluster, owner team is data-platform

Out of scope:
- Not a real-time aggregation (separate spec)
- Not changing the source schema

Output: specs/daily-aggregation/SPEC.md
```

### Refactor / migration

```
Use the spec-writer skill to create a spec for
extracting the billing module into its own package.

Context:
- Currently lives in src/server/services/billing/* and is imported from 14 sites
- Goal: same behavior, new import path @acme/billing
- No external API change
- Must support a transitional window where both import paths work
- All existing tests must continue to pass with no modifications

What this is NOT:
- Not a behavior change
- Not introducing a new persistence layer
- Not splitting the package further (future)

Output: specs/extract-billing-package/SPEC.md
```

---

## 3. By workflow

### Decomposing a large feature

When the feature is too broad for one spec, ask for a decomposition first — *before* writing any spec.

```
Read AGENTS.md and the current specs/ directory.

I need to implement a multi-tenant billing system. Before writing any spec,
produce a decomposition plan:

1. List each sub-component that needs its own spec.
2. For each: state which existing files it modifies.
3. Draw the dependency graph (which spec must complete before another can start).
4. Group specs that can be written in parallel (no shared file modifications).

Do not write any spec yet. Only the decomposition.
```

Then run the parallel group concurrently:

```
Use the spec-writer skill to create three specs in parallel.
Each runs independently — do not coordinate between them.

Spec 1:
  Feature: subscription model
  Constraints: Stripe-backed, plan tiers in DB, no free tier
  Output: specs/subscription-model/SPEC.md

Spec 2:
  Feature: payment gateway integration
  Constraints: Stripe only, idempotency keys required, webhook handling
  Output: specs/payment-gateway/SPEC.md

Spec 3:
  Feature: invoice PDF renderer
  Constraints: Puppeteer, async generation, S3 storage
  Output: specs/invoice-renderer/SPEC.md
```

Then the dependent spec, after the inputs are accepted:

```
The following specs are accepted:
- specs/subscription-model/SPEC.md
- specs/payment-gateway/SPEC.md

Use the spec-writer skill to create a spec for
the billing orchestrator that ties them together.

Context:
- Reads from subscription-model's SubscriptionStore interface
- Writes to payment-gateway's ChargeRequest contract
- Owns the retry logic and idempotency layer

Output: specs/billing-orchestrator/SPEC.md
```

### Plan mode (review before draft)

Use plan mode when the feature is exploratory, the codebase is unfamiliar to the agent, or you want to verify the agent's mental model before it commits to a full draft.

```
[Plan mode: Shift+Tab in Claude Code, or your tool's equivalent]

Use the spec-writer skill to create a spec for
the async job processing pipeline.

Context:
- PostgreSQL-backed queue (outbox pattern already in use elsewhere)
- Workers must be horizontally scalable
- Dead-letter queue required

Show me your plan first:
- Which sections of the template will be non-trivial.
- Open questions you found in the codebase.
- The Step 1 (why-this-not-that) reasoning, in summary.

Do not write the spec until I approve.
```

### Retrofitting an existing project

When the project doesn't have an `AGENTS.md` / `CLAUDE.md`, ask the agent to read enough of the codebase first.

```
The project doesn't have an AGENTS.md yet. Before drafting any spec:

1. Read README.md, package.json, the entry point, and at least 5 representative
   files in src/.
2. Summarize the architecture in 10 bullets.
3. List what you'd want documented in an AGENTS.md to spec future features.

Then use the spec-writer skill to create a spec for
adding audit logging to all admin actions.

Output: specs/audit-logging/SPEC.md
```

### Revising a draft

When a generated spec is close but not right, refine in place — don't regenerate from scratch.

```
The draft at specs/rate-limiting/SPEC.md is mostly right, but:

1. The Decisions section in §5 lists "in-memory only" as an alternative, but
   that's a strawman — we never considered it. Remove it. Add a real
   alternative we did discuss: a sidecar service (e.g., Envoy rate-limit).
2. The DoD item "rate limiter works correctly" is a feeling. Replace with
   verifiable items per requirement.
3. Add a section on what happens when the JWT is malformed — currently
   undefined behavior.

Apply spec-writer's quality bar and self-check before saving.
```

### Resolving `[NEEDS CLARIFICATION]` markers

When you come back with answers to open questions, refer to the markers explicitly so the agent knows where to apply them.

```
Resolving the open questions in specs/multitenant-isolation/SPEC.md:

- "Connection pooling: one pool per schema vs one pool with SET search_path"
  → One pool with SET search_path per query. Reason: 200+ tenants, one pool
    per schema would exhaust the connection budget.
- "How tenant context propagates into the existing async worker pool"
  → Tenant ID is serialized into the job payload. Workers SET search_path
    on dequeue.

Update §3 (Open questions and assumptions), §5 (Decisions), §6 (Design), and
§11 (Implementation outline) accordingly. Remove the [NEEDS CLARIFICATION]
markers. Re-run the self-check.
```

### Confirming (or vetoing) assumptions

Non-blocking unknowns proceed with a chosen default in the Assumptions table (§3). Review them in one pass:

```
Review the Assumptions table in specs/webhook-delivery/SPEC.md:

- "Retry schedule is fixed, not per-tenant" → confirmed.
- "Dead-lettered deliveries are retained for 30 days" → veto: make it 90 days,
  compliance requires it.
- "Signature key rotation is out of scope" → confirmed.

Flip Confirmed? to yes on the confirmed rows, apply the veto (update §4 and any
affected sections), and re-run the self-check.
```

### Adapting a spec when constraints change

```
specs/webhook-delivery/SPEC.md is accepted, but the SLA changed:
the team wants 99.9% delivery within 60 seconds for the first attempt,
not the 10 seconds in the current spec.

Update §4 (Requirements), §20 (Performance), and §21 (Testing) to reflect
the new target. Note the change in the document header (Last updated, Related)
and in §2 (Goals) if the appetite for the work changes.

Do not touch sections that are unaffected.
```

### Generating an `AGENTS.md` first (if absent)

The skill assumes a primary context document. If the project doesn't have one, generating it is a high-leverage first step.

```
This project has no AGENTS.md / CLAUDE.md. Before any spec, produce one.

Read package.json, the entry point, the routing or composition root, and
representative files in src/. Then write AGENTS.md with:

- File map: every significant file with a one-line description.
- Architecture rules: non-negotiable constraints (e.g., "interfaces at the
  consumer layer, never at infra").
- Wiring sequence: initialization order in the entry point, numbered.
- Key patterns: where each pattern lives (cache-aside, outbox, CQRS, etc.).
- Configuration reference: env vars with defaults.

Optimize for token efficiency — terse over verbose. Output: AGENTS.md
```

---

## 4. Anti-pattern prompts (what to avoid)

### Too vague — guarantees a placeholder spec

```
Use the spec-writer skill to spec out user authentication.
```

Too broad. The agent will either (a) ask 10 clarifying questions or (b) silently invent constraints. Add at least: which app, current auth state, target identity provider, session vs token, and what's out of scope.

### Asking for the world in one spec

```
Use the spec-writer skill to spec the entire admin panel,
including users, billing, audit logs, settings, and reporting.
```

This will produce a sprawling document that's impossible to verify section by section. Decompose first (see "Decomposing a large feature" above), then run focused specs.

### Specifying the answer before the spec

```
Use the spec-writer skill to write a spec that
implements OAuth with Auth0, stores sessions in Redis,
uses JWT with 7-day expiration, refresh tokens in HttpOnly cookies,
and a /me endpoint with a 5-minute cache.
```

This isn't a spec request — it's an implementation order written as a paragraph. Either pass the constraints as **Decided** in a Level 2/3 prompt (so the agent still produces the missing pieces: failure modes, tests, DoD, rollout) or just write the code.

### Tactical work — overhead is disproportionate

```
Use the spec-writer skill to fix the typo in the login button label.
```

The skill is for implementation specs. Small features are fine — the small tier produces a compact spec instead of the full template — but one-line fixes, typo corrections, and config tweaks don't need a spec at all. Just ask for the change directly.

---

## 5. Quick-reference cheatsheet

| Situation | Prompt level | Plan mode |
|---|---|---|
| New feature, well-documented project | 1 | optional |
| New feature, main decisions made | 2 | optional |
| New feature, real open decisions | 3 | recommended |
| Unfamiliar codebase, agent has no context | 2 + ask for AGENTS.md first | recommended |
| Broad initiative spanning multiple components | decomposition prompt first | recommended |
| Small feature (≤3 files, clear scope) | 1 or 2 — the small tier keeps the spec compact | not needed |
| Refining an existing draft | revision prompt, point at file | not needed |
| Resolving open questions / confirming assumptions | targeted update prompt | not needed |
| Tactical fix / one-line change | don't use the skill | — |

| Output convention | Default |
|---|---|
| Spec location | `specs/<feature-name>/SPEC.md` |
| Index file | `specs/README.md` (auto-created if absent) |
| Naming | lowercase, hyphenated, one spec per directory |
