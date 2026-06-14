import type {
  Target,
  CreateTargetRequest,
  UpdateTargetRequest,
  Scan,
  ScanDetail,
  Agent,
  CreateAgentResponse,
  AgentDownloads,
  Bundle,
  BundleControl,
  AssetListResponse,
  AssetDetailResponse,
  ControlEntry,
} from './types';

// DC API base URL is resolved at call time from the active tenant context.
// In prod, each tenant lives on a specific DC (possibly different from the
// one the frontend was deployed alongside); using the active DC URL means
// the tenant frontend works for tenants in any region.
//
// For local dev (or if no DC URL is set yet), fall back to '' which means
// same-origin (nginx proxy).
const TOKEN_KEY = 'silkstrand_token';
const DC_URL_KEY = 'silkstrand_dc_api_url';

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(DC_URL_KEY);
}

export function setDCApiURL(url: string | null | undefined): void {
  if (url) localStorage.setItem(DC_URL_KEY, url);
  else localStorage.removeItem(DC_URL_KEY);
}

function dcBaseURL(): string {
  return localStorage.getItem(DC_URL_KEY) || import.meta.env.VITE_API_URL || '';
}

export function hasToken(): boolean {
  return getToken() !== null;
}

// --- helpers kept for backwards compat with tests / legacy callers ---
export function hasDevToken(): boolean { return hasToken(); }

/**
 * request sends a JSON HTTP request to the DC API. The JWT in localStorage
 * is attached as a bearer token. On 401, the token is cleared and the
 * caller sees an error — callers render the login page from the auth
 * context, not via redirects here.
 */
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string> | undefined),
  };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const res = await fetch(`${dcBaseURL()}${path}`, { ...init, headers });

  if (res.status === 401) {
    clearToken();
    window.location.href = '/login';
    throw new Error('Unauthorized');
  }

  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error) message = body.error;
    } catch {
      /* empty */
    }
    throw new Error(message);
  }

  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// Targets
export const listTargets = () => request<Target[]>('/api/v1/targets');
export const createTarget = (req: CreateTargetRequest) =>
  request<Target>('/api/v1/targets', { method: 'POST', body: JSON.stringify(req) });
export const getTarget = (id: string) => request<Target>(`/api/v1/targets/${id}`);
export const updateTarget = (id: string, req: UpdateTargetRequest) =>
  request<Target>(`/api/v1/targets/${id}`, { method: 'PUT', body: JSON.stringify(req) });
export const deleteTarget = (id: string) =>
  request<void>(`/api/v1/targets/${id}`, { method: 'DELETE' });

// Scans
export const listScans = () => request<Scan[]>('/api/v1/scans');
// Well-known id of the global "discovery" bundle row seeded by migration
// 015. scan_type=discovery ignores the bundle contents on the agent side
// but scans.bundle_id is NOT NULL, so the UI always passes this id for
// discovery launches.
export const DISCOVERY_BUNDLE_ID = '11111111-1111-1111-1111-111111111111';

export const createScan = (
  targetId: string,
  bundleId: string,
  scanType?: 'compliance' | 'discovery',
) =>
  request<Scan>('/api/v1/scans', {
    method: 'POST',
    body: JSON.stringify({
      target_id: targetId,
      bundle_id: bundleId,
      ...(scanType ? { scan_type: scanType } : {}),
    }),
  });
export const getScan = (id: string) => request<Scan>(`/api/v1/scans/${id}`);
// #387: same endpoint, richer type — the chunk rollup + list for the live
// Scan Activity drawer's initial state.
export const getScanDetail = (id: string) =>
  request<ScanDetail>(`/api/v1/scans/${id}`);
export const deleteScan = (id: string) =>
  request<void>(`/api/v1/scans/${id}`, { method: 'DELETE' });
export const listBundles = () => request<Bundle[]>('/api/v1/bundles');

export const getBundleControls = (bundleId: string) =>
  request<BundleControl[]>(`/api/v1/bundles/${bundleId}/controls`);

export const uploadBundle = (tarball: File, signature?: File) => {
  const form = new FormData();
  form.append('tarball', tarball);
  if (signature) form.append('signature', signature);
  return fetch(`${dcBaseURL()}/api/v1/bundles/upload`, {
    method: 'POST',
    headers: { 'Authorization': `Bearer ${getToken()}` },
    body: form,
  }).then(async (res) => {
    if (res.status === 401) {
      clearToken();
      window.location.href = '/login';
      throw new Error('Unauthorized');
    }
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      throw new Error(body.error || `Upload failed: ${res.status}`);
    }
    return res.json();
  });
};

// Cross-framework control catalog (Level 2A)
export const listControls = (params?: {
  framework?: string; engine?: string; severity?: string;
  tag?: string; q?: string; page?: number; page_size?: number;
}) => {
  const u = new URLSearchParams();
  if (params?.framework) u.set('framework', params.framework);
  if (params?.engine) u.set('engine', params.engine);
  if (params?.severity) u.set('severity', params.severity);
  if (params?.tag) u.set('tag', params.tag);
  if (params?.q) u.set('q', params.q);
  if (params?.page) u.set('page', String(params.page));
  if (params?.page_size) u.set('page_size', String(params.page_size));
  const qs = u.toString();
  return request<{ items: ControlEntry[]; total: number }>(`/api/v1/controls${qs ? `?${qs}` : ''}`);
};

// Assets (ADR 003 R1a)
export interface AssetFilterParams {
  service?: string;
  service_in?: string[];
  ip?: string;
  source?: 'manual' | 'discovered';
  compliance_status?: string;
  cve_count_gte?: number;
  new_since?: string;     // e.g. "7d"
  changed_since?: string; // e.g. "7d"
  q?: string;
  sort_by?: 'last_seen' | 'first_seen' | 'ip' | 'service' | 'cve_count';
  sort_dir?: 'asc' | 'desc';
  page?: number;
  page_size?: number;
}

function buildAssetQuery(params: AssetFilterParams): string {
  const u = new URLSearchParams();
  if (params.service) u.set('service', params.service);
  if (params.service_in?.length) u.set('service_in', params.service_in.join(','));
  if (params.ip) u.set('ip', params.ip);
  if (params.source) u.set('source', params.source);
  if (params.compliance_status) u.set('compliance_status', params.compliance_status);
  if (params.cve_count_gte != null) u.set('cve_count_gte', String(params.cve_count_gte));
  if (params.new_since) u.set('new_since', params.new_since);
  if (params.changed_since) u.set('changed_since', params.changed_since);
  if (params.q) u.set('q', params.q);
  if (params.sort_by) u.set('sort_by', params.sort_by);
  if (params.sort_dir) u.set('sort_dir', params.sort_dir);
  if (params.page) u.set('page', String(params.page));
  if (params.page_size) u.set('page_size', String(params.page_size));
  return u.toString();
}

export const listAssets = (params: AssetFilterParams = {}) =>
  request<AssetListResponse>(`/api/v1/assets?${buildAssetQuery(params)}`);

export const getAsset = (id: string, eventsLimit = 50) =>
  request<AssetDetailResponse>(`/api/v1/assets/${id}?events=${eventsLimit}`);

export const promoteAsset = (id: string, bundleId: string) =>
  request<{ target: Target; bundle_id: string }>(`/api/v1/assets/${id}/promote`, {
    method: 'POST',
    body: JSON.stringify({ bundle_id: bundleId }),
  });

// ADR 014 D2: bulk-import DNS names as http_service assets. Concrete names
// become assets; wildcards are allowlist patterns only; allowlist_entries are
// what the operator must add to the agent allowlist for these to scan.
export interface ImportDNSResult {
  imported: { name: string; asset_id: string }[];
  wildcards: string[];
  skipped: { input: string; reason: string }[];
  allowlist_entries: string[];
}
export const importDNSNames = (names: string[]) =>
  request<ImportDNSResult>('/api/v1/assets/import-dns', {
    method: 'POST',
    body: JSON.stringify({ names }),
  });

// Asset endpoints — flat per-port list for the Endpoints tab.
export interface AssetEndpointRow {
  id: string;
  asset_id: string;
  host: string;
  ip: string;
  port: number;
  protocol: string;
  service?: string;
  version?: string;
  technologies?: string[];
  findings_count: number;
  coverage: { has_scan_definition: boolean; has_credential_mapping: boolean };
  last_seen: string;
}

export const listAssetEndpoints = (params?: {
  q?: string; service?: string; port?: number; source?: string;
  page?: number; page_size?: number;
}) => {
  const u = new URLSearchParams();
  if (params?.q) u.set('q', params.q);
  if (params?.service) u.set('service', params.service);
  if (params?.port) u.set('port', String(params.port));
  if (params?.source) u.set('source', params.source);
  if (params?.page) u.set('page', String(params.page));
  if (params?.page_size) u.set('page_size', String(params.page_size));
  const qs = u.toString();
  return request<{ items: AssetEndpointRow[]; total: number }>(`/api/v1/asset-endpoints${qs ? `?${qs}` : ''}`);
};

// Correlation rules (ADR 003 R1b)
import type { CorrelationRule, CorrelationRuleBody } from './types';
export const listCorrelationRules = () =>
  request<CorrelationRule[]>('/api/v1/correlation-rules');

export interface UpsertRuleRequest {
  name: string;
  trigger: 'asset_discovered' | 'asset_event';
  enabled?: boolean;
  event_type_filter?: string;
  body: CorrelationRuleBody;
}

export const createCorrelationRule = (req: UpsertRuleRequest) =>
  request<CorrelationRule>('/api/v1/correlation-rules', {
    method: 'POST',
    body: JSON.stringify(req),
  });

export const updateCorrelationRule = (id: string, req: UpsertRuleRequest) =>
  request<CorrelationRule>(`/api/v1/correlation-rules/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });

export const deleteCorrelationRule = (id: string) =>
  request<void>(`/api/v1/correlation-rules/${id}`, { method: 'DELETE' });

// Collection type + listCollections are defined in the "Collections
// (ADR 006 D5)" section below; consumed by the Credentials mapping UX here.

// ---------------------------------------------------------------
// Credential sources + mappings (ADR 004 C0 + ADR 006 P6 / P5-b)
// ---------------------------------------------------------------
//
// One consolidated surface replaces the old notification-channels API.
// `type` covers static (DB/host auth), the four integration kinds
// (slack/webhook/email/pagerduty), and the three vault kinds
// (aws_secrets_manager/hashicorp_vault/cyberark). The API scrubs
// secret fields on read to the sentinel '(set)'; update requests may
// leave secret fields blank to preserve the existing value.
export type CredentialSourceType =
  | 'static'
  | 'slack'
  | 'webhook'
  | 'email'
  | 'pagerduty'
  | 'aws_secrets_manager'
  | 'hashicorp_vault'
  | 'cyberark';

export interface CredentialSource {
  id: string;
  tenant_id: string;
  name: string;
  type: CredentialSourceType;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface UpsertCredentialSourceRequest {
  name?: string;
  type: CredentialSourceType;
  config: Record<string, unknown>;
}

export const listCredentialSources = (type?: CredentialSourceType) =>
  request<CredentialSource[]>(
    `/api/v1/credential-sources${type ? `?type=${encodeURIComponent(type)}` : ''}`,
  );

export const createCredentialSource = (req: UpsertCredentialSourceRequest) =>
  request<CredentialSource>('/api/v1/credential-sources', {
    method: 'POST',
    body: JSON.stringify(req),
  });

export const updateCredentialSource = (id: string, body: { name?: string; config: Record<string, unknown> }) =>
  request<CredentialSource>(`/api/v1/credential-sources/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  });

export const deleteCredentialSource = (id: string) =>
  request<void>(`/api/v1/credential-sources/${id}`, { method: 'DELETE' });

export const testCredentialSource = (id: string, agentId?: string) => {
  const qs = agentId ? `?agent_id=${agentId}` : '';
  return request<{ success: boolean; username?: string; error?: string; hint?: string; duration_ms?: number }>(
    `/api/v1/credential-sources/${id}/test${qs}`, { method: 'POST' },
  );
};

export type MappingScopeKind = 'collection' | 'asset_endpoint' | 'asset';

export interface CredentialMapping {
  id: string;
  tenant_id: string;
  scope_kind: MappingScopeKind;
  collection_id?: string;
  asset_endpoint_id?: string;
  asset_id?: string;
  credential_source_id: string;
  created_at: string;
}

export const listCredentialMappings = () =>
  request<CredentialMapping[]>('/api/v1/credential-mappings');

export const createCredentialMapping = (req: {
  scope_kind: MappingScopeKind;
  credential_source_id: string;
  collection_id?: string;
  asset_endpoint_id?: string;
  asset_id?: string;
}) =>
  request<CredentialMapping>('/api/v1/credential-mappings', {
    method: 'POST',
    body: JSON.stringify(req),
  });

export interface BulkCreateMappingResult {
  results: Array<{ scope_id: string; collection_id?: string; mapping_id?: string; error?: string }>;
  mapped: number;
}

export const bulkCreateCredentialMappings = (
  credentialSourceId: string,
  collectionIds: string[],
) =>
  request<BulkCreateMappingResult>('/api/v1/credential-mappings/bulk', {
    method: 'POST',
    body: JSON.stringify({
      credential_source_id: credentialSourceId,
      collection_ids: collectionIds,
    }),
  });

export const bulkCreateEndpointMappings = (
  credentialSourceId: string,
  endpointIds: string[],
) =>
  request<BulkCreateMappingResult>('/api/v1/credential-mappings/bulk', {
    method: 'POST',
    body: JSON.stringify({
      credential_source_id: credentialSourceId,
      endpoint_ids: endpointIds,
    }),
  });

export const bulkCreateAssetMappings = (
  credentialSourceId: string,
  assetIds: string[],
) =>
  request<BulkCreateMappingResult>('/api/v1/credential-mappings/bulk', {
    method: 'POST',
    body: JSON.stringify({
      credential_source_id: credentialSourceId,
      asset_ids: assetIds,
    }),
  });

export const deleteCredentialMapping = (id: string) =>
  request<void>(`/api/v1/credential-mappings/${id}`, { method: 'DELETE' });

// ─── Collections (ADR 006 D5) ────────────────────────────────────────────────
// Collections replace Asset Sets. Until P4-backend lands and/or the
// /api/v1/asset-sets alias is deprecated, these client methods target the
// new /api/v1/collections surface. See docs/plans/asset-first-execution.md.
import type {
  Collection,
  CollectionPreview,
  CollectionScope,
  AssetEndpoint,
  WidgetKind,
} from './types';

export interface UpsertCollectionRequest {
  name: string;
  description?: string;
  scope: CollectionScope;
  predicate: Record<string, unknown>;
  is_dashboard_widget?: boolean;
  widget_kind?: WidgetKind;
  widget_title?: string;
}

export const listCollections = (params?: { widget?: boolean; scope?: CollectionScope }) => {
  const u = new URLSearchParams();
  if (params?.widget) u.set('widget', 'true');
  if (params?.scope) u.set('scope', params.scope);
  const qs = u.toString();
  return request<Collection[]>(`/api/v1/collections${qs ? `?${qs}` : ''}`);
};

export const getCollection = (id: string) =>
  request<Collection>(`/api/v1/collections/${id}`);

export const createCollection = (req: UpsertCollectionRequest) =>
  request<Collection>('/api/v1/collections', {
    method: 'POST',
    body: JSON.stringify(req),
  });

export const updateCollection = (id: string, req: UpsertCollectionRequest) =>
  request<Collection>(`/api/v1/collections/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });

export const deleteCollection = (id: string) =>
  request<void>(`/api/v1/collections/${id}`, { method: 'DELETE' });

export const previewCollection = (id: string) =>
  request<CollectionPreview>(`/api/v1/collections/${id}/preview`);

export const previewAdhocCollection = (
  scope: CollectionScope,
  predicate: Record<string, unknown>,
) =>
  request<CollectionPreview>('/api/v1/collections/preview', {
    method: 'POST',
    body: JSON.stringify({ scope, predicate }),
  });

export const getCollectionMembers = (id: string, params?: { page?: number; page_size?: number }) => {
  const u = new URLSearchParams();
  if (params?.page) u.set('page', String(params.page));
  if (params?.page_size) u.set('page_size', String(params.page_size));
  const qs = u.toString();
  return request<{ items: Array<Record<string, unknown>>; total: number }>(
    `/api/v1/collections/${id}/members${qs ? `?${qs}` : ''}`,
  );
};

// Asset endpoint fetch — drives the Asset drawer's expand-on-click row.
export const getAssetEndpoint = (endpointId: string) =>
  request<AssetEndpoint & {
    findings?: Array<{ id: string; title: string; severity: string; source: string; status: string }>;
    last_scan_at?: string;
    next_scan_at?: string;
    last_error?: string;
  }>(`/api/v1/asset-endpoints/${endpointId}`);

// Bulk credential mapping — applied to a selection of endpoints.
export const bulkMapCredentials = (req: {
  endpoint_ids: string[];
  credential_source_id: string;
}) =>
  request<{ mapped: number }>('/api/v1/credential-mappings/bulk', {
    method: 'POST',
    body: JSON.stringify(req),
  });

// ─── end Collections ─────────────────────────────────────────────────────────

// Agents
export const listAgents = () => request<Agent[]>('/api/v1/agents');
export const createAgent = (name: string, version?: string) =>
  request<CreateAgentResponse>('/api/v1/agents', {
    method: 'POST',
    body: JSON.stringify({ name, version }),
  });
export const rotateAgentKey = (id: string) =>
  request<{ api_key: string }>(`/api/v1/agents/${id}/rotate-key`, { method: 'POST' });
export const deleteAgent = (id: string) =>
  request<void>(`/api/v1/agents/${id}`, { method: 'DELETE' });
export const upgradeAgent = (id: string, version?: string) =>
  request<{ status: string; version: string }>(`/api/v1/agents/${id}/upgrade`, {
    method: 'POST',
    body: JSON.stringify({ version: version ?? 'latest' }),
  });
export const getAgentDownloads = () =>
  request<AgentDownloads>('/api/v1/agents/downloads');

export interface AgentAllowlist {
  agent_id: string;
  snapshot_hash: string;
  allow: string[];
  deny: string[];
  rate_limit_pps: number;
  reported_at: string;
  updated_at: string;
}

export const getAgentAllowlist = (id: string) =>
  request<AgentAllowlist>(`/api/v1/agents/${id}/allowlist`);

// Agent log events (persisted console lines)
export interface AgentLogEntry {
  id: string;
  agent_id: string;
  scan_id?: string;
  level: string;
  msg: string;
  attrs: Record<string, unknown>;
  occurred_at: string;
}

export const getAgentLogs = (agentId: string, params?: {
  since?: string; until?: string; level?: string;
  scan_id?: string; limit?: number; order?: 'asc' | 'desc';
}) => {
  const u = new URLSearchParams();
  if (params?.since) u.set('since', params.since);
  if (params?.until) u.set('until', params.until);
  if (params?.level) u.set('level', params.level);
  if (params?.scan_id) u.set('scan_id', params.scan_id);
  if (params?.limit) u.set('limit', String(params.limit));
  if (params?.order) u.set('order', params.order);
  const qs = u.toString();
  return request<{ items: AgentLogEntry[]; total: number }>(
    `/api/v1/agents/${agentId}/logs${qs ? `?${qs}` : ''}`,
  );
};
export const createInstallToken = (opts?: {
  auto_discover?: boolean;
  discover_schedule?: 'off' | 'daily' | 'weekly';
  zone?: string;
}) =>
  request<{ install_token: string; expires_at: string }>('/api/v1/agents/install-tokens', {
    method: 'POST',
    body: JSON.stringify(opts ?? {}),
  });

// ADR 013 D6: install-time overlap heuristic. Warn (never block) when the
// ranges about to be seeded overlap another agent that actually discovers.
export interface AllowlistOverlap {
  cidr: string;
  conflicts_with: {
    kind: string;
    name: string;
    id: string;
    range: string;
    zone: string | null;
    discovery_enabled: boolean;
  };
}
export interface AllowlistPreview {
  overlaps: AllowlistOverlap[];
  redundant: string[];
}
export const previewAllowlist = (cidrs: string[], zone?: string) =>
  request<AllowlistPreview>('/api/v1/agents/allowlist-preview', {
    method: 'POST',
    body: JSON.stringify({ cidrs, zone }),
  });

// Credentials (per-target)
export const getTargetCredential = (targetId: string) =>
  request<{ set: boolean; type?: string }>(`/api/v1/targets/${targetId}/credential`);
export const putTargetCredential = (targetId: string, type: string, data: Record<string, unknown>) =>
  request<void>(`/api/v1/targets/${targetId}/credential`, {
    method: 'PUT',
    body: JSON.stringify({ type, data }),
  });
export const deleteTargetCredential = (targetId: string) =>
  request<void>(`/api/v1/targets/${targetId}/credential`, { method: 'DELETE' });
export const probeTarget = (targetId: string) =>
  request<{ ok: boolean; error?: string; detail?: string }>(
    `/api/v1/targets/${targetId}/probe`,
    { method: 'POST' },
  );

// ─── ADR 011 — Collected facts + policy source ───────────────────────────

export interface CollectedFactsEntry {
  collector_id: string;
  facts: Record<string, unknown>;
  collected_at: string;
}

export const getScanFacts = (scanId: string) =>
  request<{ items: CollectedFactsEntry[] }>(`/api/v1/scans/${scanId}/facts`);

export const getControlRego = (controlId: string) =>
  request<{ rego_source: string }>(`/api/v1/controls/${controlId}/rego`);

export const copyTenantPolicy = (controlId: string) =>
  request<{ id: string }>(`/api/v1/tenant-policies/copy-from/${controlId}`, { method: 'POST' });

// ─── ADR 007 — Scan definitions + Findings ───────────────────────────────
import type {
  ScanDefinition,
  ScanDefinitionKind,
  ScanDefinitionScopeKind,
  ScanDefinitionCoverage,
  Finding,
  FindingSourceKind,
  FindingStatus,
} from './types';

export interface UpsertScanDefinitionRequest {
  name: string;
  kind: ScanDefinitionKind;
  bundle_id?: string;
  scope_kind: ScanDefinitionScopeKind;
  asset_endpoint_id?: string;
  collection_id?: string;
  cidr?: string;
  agent_id?: string;
  schedule?: string | null;
  enabled?: boolean;
}

export const listScanDefinitions = () =>
  request<ScanDefinition[]>('/api/v1/scan-definitions');

export const getScanDefinition = (id: string) =>
  request<ScanDefinition>(`/api/v1/scan-definitions/${id}`);

export const createScanDefinition = (req: UpsertScanDefinitionRequest) =>
  request<ScanDefinition>('/api/v1/scan-definitions', {
    method: 'POST',
    body: JSON.stringify(req),
  });

export const updateScanDefinition = (id: string, req: UpsertScanDefinitionRequest) =>
  request<ScanDefinition>(`/api/v1/scan-definitions/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });

export const deleteScanDefinition = (id: string) =>
  request<void>(`/api/v1/scan-definitions/${id}`, { method: 'DELETE' });

export const executeScanDefinition = (id: string) =>
  request<{ scan_id: string }>(`/api/v1/scan-definitions/${id}/execute`, {
    method: 'POST',
  });

export const enableScanDefinition = (id: string) =>
  request<ScanDefinition>(`/api/v1/scan-definitions/${id}/enable`, { method: 'POST' });

export const disableScanDefinition = (id: string) =>
  request<ScanDefinition>(`/api/v1/scan-definitions/${id}/disable`, { method: 'POST' });

export const getScanDefinitionCoverage = (id: string) =>
  request<ScanDefinitionCoverage>(`/api/v1/scan-definitions/${id}/coverage`);

export interface FindingFilterParams {
  source_kind?: FindingSourceKind | FindingSourceKind[];
  source?: string;
  severity?: string;
  status?: FindingStatus;
  asset_endpoint_id?: string;
  collection_id?: string;
  cve_id?: string;
  scan_id?: string;
  since?: string;
  until?: string;
  q?: string;
  page?: number;
  page_size?: number;
}

function buildFindingQuery(params: FindingFilterParams): string {
  const u = new URLSearchParams();
  if (params.source_kind) {
    const kinds = Array.isArray(params.source_kind)
      ? params.source_kind
      : [params.source_kind];
    kinds.forEach((k) => u.append('source_kind', k));
  }
  if (params.source) u.set('source', params.source);
  if (params.severity) u.set('severity', params.severity);
  if (params.status) u.set('status', params.status);
  if (params.asset_endpoint_id) u.set('asset_endpoint_id', params.asset_endpoint_id);
  if (params.collection_id) u.set('collection_id', params.collection_id);
  if (params.cve_id) u.set('cve_id', params.cve_id);
  if (params.scan_id) u.set('scan_id', params.scan_id);
  if (params.since) u.set('since', params.since);
  if (params.until) u.set('until', params.until);
  if (params.q) u.set('q', params.q);
  if (params.page) u.set('page', String(params.page));
  if (params.page_size) u.set('page_size', String(params.page_size));
  return u.toString();
}

export const listFindings = (params: FindingFilterParams = {}) => {
  const qs = buildFindingQuery(params);
  return request<Finding[]>(`/api/v1/findings${qs ? `?${qs}` : ''}`);
};

export const getFinding = (id: string) =>
  request<Finding>(`/api/v1/findings/${id}`);

export const suppressFinding = (id: string) =>
  request<Finding>(`/api/v1/findings/${id}/suppress`, { method: 'POST' });

export const reopenFinding = (id: string) =>
  request<Finding>(`/api/v1/findings/${id}/reopen`, { method: 'POST' });

// ──────────────────────────────────────────────────────────────────────
// P5-a: Dashboard (BOUNDED SECTION — shared-conflict file).
// Keep additions inside this block; other workstreams add to their own
// BOUNDED sections to minimise merge conflicts.
// ──────────────────────────────────────────────────────────────────────

export interface DashboardKpis {
  total_assets: number;
  coverage_percent: number;
  critical_findings: number;
  new_this_week: number;
  deltas: {
    assets_new_this_week: number;
    findings_new_today: number;
    coverage_delta_week: number;
    unresolved_new_week: number;
  };
}

export interface SuggestedAction {
  kind:
    | 'endpoints_missing_credentials'
    | 'assets_without_scans'
    | 'recent_scan_failures';
  title: string;
  count: number;
  collection_id_or_inline_predicate: string;
  primary_cta: string;
  secondary_cta: string;
}

export interface RecentActivityItem {
  id: string;
  event_type: string;
  severity?: string;
  asset_endpoint_id: string;
  hostname?: string;
  primary_ip?: string;
  port?: number;
  service?: string;
  occurred_at: string;
}

export const getDashboardKpis = () =>
  request<DashboardKpis>('/api/v1/dashboard/kpis');

export const getSuggestedActions = () =>
  request<{ items: SuggestedAction[] }>('/api/v1/dashboard/suggested-actions');

export const getRecentActivity = () =>
  request<{ items: RecentActivityItem[] }>('/api/v1/dashboard/recent-activity');

// ──────────────────────────────────────────────────────────────────────
// ADR 008 PR E — events stream token mint.
// `POST /api/v1/events/stream-tokens` accepts an optional filter that
// is baked into the resulting 60s stream token. The browser EventSource
// can't set headers, so this token (typ=stream) is the authentication
// mechanism for the SSE endpoint `/api/v1/events/stream?token=…`.
//
// Note: the backend (`api/internal/handler/events.go`) uses a `kinds`
// array (plural) on the filter — we mirror that exact shape here, not
// the `kind` singular that appears in the SSE query string.
// ──────────────────────────────────────────────────────────────────────

export interface StreamTokenRequest {
  kinds?: string[];
  resource_type?: string;
  resource_id?: string;
  scan_id?: string;
}

export interface StreamTokenResponse {
  token: string;
  expires_at: string;
}

export const mintStreamToken = (filter?: StreamTokenRequest) =>
  request<StreamTokenResponse>('/api/v1/events/stream-tokens', {
    method: 'POST',
    body: JSON.stringify({ filter: filter ?? {} }),
  });

// --- Compliance profiles (Level 3A) ---

export interface ComplianceProfile {
  id: string;
  tenant_id: string;
  name: string;
  description?: string;
  base_framework?: string;
  status: 'draft' | 'published';
  version: number;
  bundle_id?: string;
  control_count: number;
  created_at: string;
  updated_at: string;
}

export const listComplianceProfiles = () =>
  request<ComplianceProfile[]>('/api/v1/compliance-profiles');

export const createComplianceProfile = (req: { name: string; description?: string; base_framework?: string }) =>
  request<ComplianceProfile>('/api/v1/compliance-profiles', { method: 'POST', body: JSON.stringify(req) });

export const getComplianceProfile = (id: string) =>
  request<ComplianceProfile>(`/api/v1/compliance-profiles/${id}`);

export const updateComplianceProfile = (id: string, req: { name?: string; description?: string }) =>
  request<ComplianceProfile>(`/api/v1/compliance-profiles/${id}`, { method: 'PUT', body: JSON.stringify(req) });

export const deleteComplianceProfile = (id: string) =>
  request<void>(`/api/v1/compliance-profiles/${id}`, { method: 'DELETE' });

export const getProfileControls = (id: string) =>
  request<string[]>(`/api/v1/compliance-profiles/${id}/controls`);

export const setProfileControls = (id: string, controlIds: string[]) =>
  request<void>(`/api/v1/compliance-profiles/${id}/controls`, {
    method: 'POST', body: JSON.stringify({ control_ids: controlIds }),
  });

export const publishProfile = (id: string) =>
  request<ComplianceProfile>(`/api/v1/compliance-profiles/${id}/publish`, { method: 'POST' });

// ─── ADR 005 — Audit events ─────────────────────────────────────────────────

export interface AuditEvent {
  id: string;
  tenant_id: string;
  occurred_at: string;
  event_type: string;
  actor_type: string;
  actor_id?: string;
  resource_type?: string;
  resource_id?: string;
  payload: Record<string, unknown>;
}

export interface AuditEventsResponse {
  items: AuditEvent[];
  next_cursor?: string;
}

export const listAuditEvents = (params?: {
  event_type?: string; actor_id?: string; resource_id?: string;
  resource_type?: string; since?: string; until?: string;
  limit?: number; cursor?: string;
}) => {
  const u = new URLSearchParams();
  if (params?.event_type) u.set('event_type', params.event_type);
  if (params?.actor_id) u.set('actor_id', params.actor_id);
  if (params?.resource_id) u.set('resource_id', params.resource_id);
  if (params?.resource_type) u.set('resource_type', params.resource_type);
  if (params?.since) u.set('since', params.since);
  if (params?.until) u.set('until', params.until);
  if (params?.limit) u.set('limit', String(params.limit));
  if (params?.cursor) u.set('cursor', params.cursor);
  const qs = u.toString();
  return request<AuditEventsResponse>(`/api/v1/audit-events${qs ? `?${qs}` : ''}`);
};

// ─── end Audit events ───────────────────────────────────────────────────────

// ─── ADR 011 D9 — Tenant policies ──────────────────────────────────────────

export interface TenantPolicy {
  id: string;
  tenant_id: string;
  control_id: string;
  origin: 'derived' | 'custom';
  based_on?: string;
  name: string;
  severity: string;
  rego_source: string;
  collector_id: string;
  fact_keys: string[];
  frameworks: Array<{ id: string; section: string; title: string }>;
  tags: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export const listTenantPolicies = () =>
  request<TenantPolicy[]>('/api/v1/tenant-policies');

export const getTenantPolicy = (id: string) =>
  request<TenantPolicy>(`/api/v1/tenant-policies/${id}`);

export const createTenantPolicy = (req: Partial<TenantPolicy>) =>
  request<TenantPolicy>('/api/v1/tenant-policies', {
    method: 'POST',
    body: JSON.stringify(req),
  });

export const updateTenantPolicy = (id: string, req: Partial<TenantPolicy>) =>
  request<TenantPolicy>(`/api/v1/tenant-policies/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });

export const deleteTenantPolicy = (id: string) =>
  request<void>(`/api/v1/tenant-policies/${id}`, { method: 'DELETE' });

export const copyBuiltinPolicy = (controlId: string) =>
  request<TenantPolicy>(`/api/v1/tenant-policies/copy-from/${controlId}`, {
    method: 'POST',
  });

export const validateRego = (rego_source: string) =>
  request<{ valid: boolean; error?: string }>('/api/v1/tenant-policies/validate', {
    method: 'POST',
    body: JSON.stringify({ rego_source }),
  });

// ─── end Tenant policies ────────────────────────────────────────────────────

// ─── Retroactive evaluation (ADR 011 D10) ───────────────────────────────────

import type { ReplayRequest, ReplayResponse } from './types';

export const replayEvaluation = (req: ReplayRequest) =>
  request<ReplayResponse>('/api/v1/evaluations/replay', {
    method: 'POST',
    body: JSON.stringify(req),
  });

// ─── end Retroactive evaluation ─────────────────────────────────────────────

// Absolute SSE URL builder — combines the active DC base URL with the
// /api/v1/events/stream path and the minted token. Exposed so
// `useEventStream` can construct a URL without duplicating the
// DC-URL resolution logic that lives here.
export function eventStreamURL(token: string): string {
  return `${dcBaseURL()}/api/v1/events/stream?token=${encodeURIComponent(token)}`;
}

