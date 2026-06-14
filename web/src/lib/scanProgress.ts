// #387: pure merge logic for the live Scan Activity drawer. Kept out of the
// component file so it stays unit-testable and doesn't trip react-refresh's
// only-export-components rule. Seeds from the ScanDetail REST snapshot, then
// folds scan_progress SSE payloads on top.
import type { ScanDetail, ScanProgressPayload } from '../api/types';

export const STAGES = ['naabu', 'httpx', 'nuclei'] as const;
const stageOrder = (s?: string) => (s ? STAGES.indexOf(s as (typeof STAGES)[number]) : -1);

const isTerminal = (s: string) => s === 'completed' || s === 'failed';

export interface ChunkView {
  key: string;
  chunkIndex?: number;
  targetIdentifier?: string;
  status: string;
  currentStage?: string;
  hostsScanned?: number;
  assetsFound?: number;
  errorMessage?: string;
}

export interface ProgressView {
  status: string;
  chunksTotal: number;
  chunksCompleted: number;
  chunksFailed: number;
  chunks: ChunkView[];
  hasChunkModel: boolean; // false for compliance (chunks_total=0)
}

// chunkKey matches a live event to a seeded chunk: chunk_id is the stable key;
// fall back to the index, and finally to the single implicit (non-chunked) unit.
function chunkKey(id?: string, index?: number): string {
  if (id) return id;
  if (index != null) return `idx-${index}`;
  return 'implicit';
}

export function mergeProgress(
  data: ScanDetail | undefined,
  payloads: ScanProgressPayload[],
): ProgressView | null {
  if (!data) return null;

  const map = new Map<string, ChunkView>();
  for (const c of data.chunks ?? []) {
    const key = chunkKey(c.id, c.chunk_index);
    map.set(key, {
      key,
      chunkIndex: c.chunk_index,
      targetIdentifier: c.target_identifier,
      status: c.status,
      currentStage: c.current_stage,
      hostsScanned: c.hosts_scanned,
      assetsFound: c.assets_found,
      errorMessage: c.error_message,
    });
  }

  let status = data.status;
  let chunksTotal = data.chunks_total;
  let chunksCompleted = data.chunks_completed;
  let chunksFailed = data.chunks_failed;

  // If the REST snapshot already shows the scan terminal, it is authoritative
  // (it incorporates every event up to its generation). Older buffered
  // non-terminal events — the missed-final-event reconnect case the 15s REST
  // refetch exists to cover — must not revert it to running, so skip them.
  const restTerminal = isTerminal(data.status);

  // Events arrive oldest-first (rolling buffer); apply in order.
  for (const p of payloads) {
    if (restTerminal && !isTerminal(p.status)) continue;
    status = p.status;
    chunksTotal = p.chunks_total;
    chunksCompleted = p.chunks_completed;
    chunksFailed = p.chunks_failed;
    if (!p.chunk) continue;
    const key = chunkKey(p.chunk.chunk_id, p.chunk.chunk_index);
    const prev = map.get(key) ?? { key, status: 'pending' };
    map.set(key, {
      key,
      chunkIndex: p.chunk.chunk_index ?? prev.chunkIndex,
      targetIdentifier: p.chunk.target_identifier ?? prev.targetIdentifier,
      status: p.chunk.status ?? prev.status,
      currentStage: p.chunk.current_stage ?? prev.currentStage,
      // Counts are authoritative only at chunk_completed (sent then, incl. 0).
      hostsScanned: p.chunk.hosts_scanned ?? prev.hostsScanned,
      assetsFound: p.chunk.assets_found ?? prev.assetsFound,
      errorMessage: p.chunk.error_message ?? prev.errorMessage,
    });
  }

  const chunks = [...map.values()].sort(
    (a, b) => (a.chunkIndex ?? 0) - (b.chunkIndex ?? 0),
  );

  // Non-chunked discovery is one implicit unit (chunks_total=1) — show a single
  // row even before any chunk-scoped event arrives. Compliance (total=0) shows
  // no chunk breakdown.
  const hasChunkModel = chunksTotal > 0;
  if (hasChunkModel && chunks.length === 0 && chunksTotal === 1) {
    chunks.push({ key: 'implicit', chunkIndex: 0, status });
  }

  // Once the scan is terminal no chunk should still read running. Real chunks
  // get their own chunk_completed/chunk_failed, but the implicit non-chunked
  // unit has no per-chunk terminal event — and a missed event could strand a
  // real chunk too — so settle any straggler to the final scan status.
  if (isTerminal(status)) {
    for (const c of chunks) {
      if (!isTerminal(c.status)) c.status = status;
    }
  }

  return { status, chunksTotal, chunksCompleted, chunksFailed, chunks, hasChunkModel };
}

export function stageState(stage: string, chunk: ChunkView): 'done' | 'active' | 'pending' {
  if (chunk.status === 'completed') return 'done';
  const si = stageOrder(stage);
  const ci = stageOrder(chunk.currentStage);
  if (ci < 0) return 'pending';
  if (si < ci) return 'done';
  if (si === ci) return chunk.status === 'failed' ? 'done' : 'active';
  return 'pending';
}
