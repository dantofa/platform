Items marked `[DONE]` are complete. Items marked `[DEFERRED]` are intentionally postponed with a noted reason

---

## Business: Operations

- Activate Microsoft 365 Business Premium (4 users)
  - Attach a primary domain (dantofa.dev, dantofa.com, or dantofa.io) in M365 admin portal
  - Verify domain ownership and configure MX/SPF/DKIM/DMARC records
  - Create accounts for all team members and enforce MFA via Conditional Access baseline policy
- Configure Entra ID as an external Identity Provider in Zitadel
  - Prerequisite: M365 Business Premium tenant active with Entra ID P1
  - Register Zitadel as an Enterprise Application in Entra (OIDC preferred; SAML as fallback)
  - Map Entra group claims to Zitadel roles for tenant access control
  - Validate end-to-end flow: Entra SSO login → Zitadel → tenant application
  - Document the setup as a reference implementation — this is the integration path B2B enterprise tenants will follow

---

## Infrastructure: Cluster and GitOps

- Add Zitadel admin OIDC support for authentication using Gitlab
- Add testing for tenant Zitadel organizations

---

## Infrastructure: Security

- Implement automated rotation for Zitadel machine user PATs
  - Current: PAT is created once and stored in the `auth-credentials` Secret; never rotated
  - Target: automated rotation on a configurable schedule or on PAT expiry detection via the drift timer
- Update grpc dependency across Flux, cloudflare-tunnel-ingress-controller, and trivy-operator charts once CVE-2026-33186 patches are released; remove entry from `.trivyignore-cluster`
- Update Alpine OpenSSL base image in trivy-operator and Zitadel charts once CVE-2026-31789 patches are released; remove entry from `.trivyignore-cluster`
- Update CloudNativePG postgres image once CVE-2026-33845 / CVE-2026-42010 (libgnutls30t64) patches are available; remove entries from `.trivyignore-cluster` (cpc-bridge-proxy entry cannot be removed — unmanaged DOKS infra)
- Update Zitadel chart once CVE-2026-41242 (protobufjs) is patched upstream; remove entry from `.trivyignore-cluster`
- Broaden automated secret rotation beyond Zitadel to the platform's own credentials: the Bitwarden machine-account token (ESO secret-zero), the DigitalOcean API token, and the Cloudflare API token
- Configure Cloudflare edge security to complement the origin IP allowlist: WAF managed rules, rate limiting, and bot management
- Establish the platform's own compliance/audit posture (we sell ISO compliance): periodic access reviews, immutable audit logging for admin/operator actions, and a documented data-handling and retention policy

---

## Infrastructure: Automation and Secrets

- Migrate Zitadel masterkey to HashiCorp Vault via ESO push mode
  - Currently the one approved manual exception to the "automate everything" rule
  - Tracked in `flux/management/infrastructure/services/zitadel/masterkey.yaml`
  - Requires: Vault deployment in-cluster integrated with ESO
  - Until complete: masterkey must not have a non-zero `refreshInterval` — changing it requires a full database re-encryption
- Add Zitadel masterkey rotation action to dantofa-ctl tool

---

## Infrastructure: Tenant Model

- Create tenant operator
- Test tenant provisioning and deprovisioning process
- Create the inception tenant
  - Reserved name (e.g. `inception`); provisioned before any agent is deployed to other tenants
  - Purpose: platform admins dogfood every agent before it reaches B2C/B2B tenants
  - Must be listed in `flux/management/tenants/` and excluded from any public discovery
- Implement B2B private tenant invitation flow
  - Admin-only action — no self-serve path
  - Flow: admin creates Tenant CR → operator provisions namespace + Zitadel org → invitation email sent via Zitadel
  - Define invitation acceptance UX (landing page in tenant frontend)
- Implement feature gating on the Tenant CRD
  - Add `spec.features` field: list of enabled feature identifiers (e.g. `iso-compliance-rag`, `multi-user-chat`)
  - In-cluster enforcement: evaluate Kyverno generate+validate vs. custom admission webhook — document selection rationale
  - Operator must not deploy feature workloads to tenants that do not have the feature enabled
- Create tenant management application on inception tenant
  - Supports root (B2C), B2B private, and inception tenants
  - Integrates with Zitadel OIDC using per-tenant `auth-credentials` Secret
  - Tech stack TBD — evaluate Next.js vs. SvelteKit; document selection rationale
- Create free and paid tenant plans with different resource allocations and features
  - Define plan tiers and feature matrix (links to `spec.features` gating)
  - Configure namespace-level ResourceQuotas per plan
- Configure resources according to tenant plan (CPU, memory, storage)
- Set up Stripe billing integration for tenant subscriptions
  - Trigger: tenant plan change → Stripe subscription update
  - Evaluate: billing microservice vs. managed Stripe webhook handler; document selection
- [DEFERRED: No need to implement policies with no tenant traffic] Implement network policies for tenant namespace isolation
  - Default-deny all cross-namespace pod-to-pod traffic
  - Allow: tenant workloads → `zitadel-system` (auth), tenant workloads → shared agent infrastructure (if any)
  - Deny: tenant → tenant (unconditionally)
  - Must complement existing Kyverno policies — not replace them

---

## AI Platform: Model Routing

- Implement LLM query router per `docs/routing.MD` specification
  - Prerequisite: conduct arxiv literature review on query classification techniques; document in `docs/`
  - Query classifier: simple factual / multi-standard comparative / scenario-based / gap assessment
  - Pluggable model provider interface — configuration-driven, not hard-coded
  - External model config (YAML/ConfigMap): provider, model ID, cost per token, context window, daily query limit
  - Routing rules in config — runtime switchable without redeployment
  - Per-query decisions based on classification + token budget + cost thresholds
  - Distributed session cache (Redis or Memcached) with configurable TTL (default: 1 hour)
  - Cache key: session ID + standard name + clause reference; track cache hits/misses for cost analysis
  - Multi-provider support: Anthropic Claude, OpenAI GPT, Google Gemini, DeepSeek, community-hosted
- Implement per-tenant model policy
  - Tenants may restrict routing to approved providers or specific model tiers
  - Policy stored in Tenant CR or tenant-namespace ConfigMap — design decision required

---

## AI Platform: Observability

- Implement OpenTelemetry instrumentation for all agent queries (per `docs/routing.MD`)
  - Parent span per query lifecycle with child spans: classification, retrieval, model selection, execution
  - Required span attributes: timestamp, session ID, user ID, query text, query type, selected model, provider, input tokens, output tokens, latency (ms), cache hit/miss, retrieved document refs, accuracy score (when available), computed cost
  - Metrics: query counters per model, latency histograms per query type, active cache gauge, token consumption per model per time window, cache hit/miss counters
  - Async emission with bounded queue (max latency overhead: 50ms)
  - Configurable exporter at runtime (OTLP HTTP/gRPC, Datadog Agent, Splunk HEC, ELK/Elasticsearch) — no code changes required to switch
- Create platform admin dashboards
  - Cost per tenant per model, latency by model and query type, accuracy trends, cache efficiency
  - SLO compliance view (see Platform Observability and Control Loops below)

---

## AI Platform: Accuracy Evaluation

- Design and implement AI accuracy evaluation framework
  - Prerequisite: conduct arxiv literature review on RAG evaluation techniques (e.g. RAGAS, TruLens, domain-specific evals); document findings and selected approach in `docs/`
  - Framework must: score each agent response, track scores over time per model per tenant, detect performance degradation
  - Alert platform admins when accuracy drops below configured threshold per model per tenant
  - This is the closed feedback loop for the AI layer — treat as mission-critical
- Build ground-truth dataset for ISO compliance RAG evaluation
  - Curate known-good question-answer pairs covering ISO 9001, ISO 27001, ISO 14001 clauses
  - Store in a versioned, auditable format (git-tracked, not in a database)
- Implement automated evaluation runs on a schedule (nightly or per deployment)
  - [DEFERRED: evaluation loop] Automated scheduled evaluation is deferred until the ground-truth dataset is sufficiently large (target: minimum 100 QA pairs per standard). Manual spot-checks by platform admins using the inception tenant serve as interim signal.

---

## AI Platform: ISO Compliance RAG Agent

- Select and deploy vector database
  - Candidates: pgvector (CloudNativePG extension — preferred, avoids new operator), Qdrant, Weaviate, Pinecone
  - Conduct OSS evaluation: maturity, Kubernetes operability, query performance; document selection rationale
- Build document ingestion pipeline for ISO standard documents
  - Prerequisite: arxiv review of chunking strategies (fixed-size vs. semantic vs. hierarchical); document findings
  - Embedding model selection: evaluate open-weight vs. API-based options; document selection
  - Pipeline must be re-runnable as new standard versions are published (idempotent, versioned)
- Implement RAG query handler
  - Simple query path: retrieve specific clauses → pass to selected model with citation instructions
  - Complex query path: retrieve broader sections or multiple related clauses
  - All retrieved text is passed read-only — no write path exists
- Implement single-user chat session
  - Session-bound context window (uses cache from router spec)
  - Chat history persisted for audit trail
- Implement multi-user chat session
  - Multiple participants in a single audit session
  - Shared session context, individual message attribution
  - Define access control: who can invite collaborators to a session
- Implement user feedback collection
  - Per-response thumbs up/down + optional free-text comment
  - Feedback stored and linked to the corresponding telemetry span (for accuracy evaluation correlation)
  - Feedback must not trigger any model action — agent is read-only; feedback is passive signal only
- Deploy to inception tenant first; validate with platform admins before any other tenant rollout
- Write integration tests covering: document retrieval accuracy, routing decisions, feedback persistence, session isolation between users
- [DEFERRED: accuracy evaluation loop] Automated accuracy evaluation for the RAG agent is deferred until the evaluation framework and ground-truth dataset are ready. Interim: manual review by platform admins via the inception tenant.

---

## Infrastructure: Platform Observability and Control Loops

- Set up monitoring and logging for the Kubernetes cluster (infrastructure-level)
  - Metrics: node CPU/memory/disk, pod restarts, Flux reconciliation lag, ESO sync failures
  - Logs: centralized log aggregation for operator, Zitadel, and all agent workloads
  - Alerting: production incident notifications (evaluate PagerDuty, Opsgenie, or equivalent)
- Implement operator health metrics
  - Track: tenant provisioning duration, Zitadel API error rate, drift correction frequency per tenant
  - Alert on: repeated provisioning failures, Zitadel API degradation
  - [DEFERRED: operator evaluation loop] Formal measurement of operator reconciliation accuracy is deferred. The 5-minute drift timer corrects divergence but does not emit a structured success/failure metric. Add reconciliation outcome metrics and a dashboard when the observability backend is deployed.
- Implement Flux reconciliation alerting
  - Alert when any Kustomization fails to reconcile for more than N minutes (define threshold)
  - Alert when deployed image tags drift from expected values
- Define and track SLOs
  - Tenant provisioning time: target < 2 minutes from CR creation to `phase=Ready`
  - Agent query latency: target p95 < TBD ms per query type (set after baseline measurement)
  - Authentication availability: Zitadel uptime target (define based on SLA requirements)

---

## AI Platform: Model Security and Safeguards

- Implement AI safeguards against scope misuse and data exfiltration
  - Query scope enforcement: classifier must reject out-of-domain queries; configurable per-tenant scope policy
  - Output length limits: agent responses must not include raw document text beyond configured citation length
  - Tamper prevention: no agent has write access to the document store or any tenant data
  - Rate limiting: per-session, per-tenant, per-user query limits enforced at the router layer
  - Audit log: every query and response logged with full attribution (user ID, session ID, tenant ID) — immutable append-only store
- Conduct security review of tenant operator RBAC
  - Operator currently has cluster-wide access to create namespaces and secrets; audit and tighten to minimum required permissions
