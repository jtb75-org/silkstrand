import { describe, it, expect } from 'vitest';
import { mergeProgress, stageState } from './scanProgress';
import type { ScanDetail, ScanProgressPayload } from '../api/types';

function detail(over: Partial<ScanDetail>): ScanDetail {
  return {
    id: 'scan-1',
    tenant_id: 't',
    bundle_id: 'b',
    status: 'running',
    created_at: '2026-06-13T00:00:00Z',
    chunks_total: 0,
    chunks_completed: 0,
    chunks_failed: 0,
    chunks: [],
    ...over,
  };
}

describe('mergeProgress', () => {
  it('folds a chunk_completed event onto the seeded chunk and uses the event rollup', () => {
    const data = detail({
      scan_type: 'discovery',
      chunks_total: 2,
      chunks: [
        { id: 'c0', scan_id: 'scan-1', chunk_index: 0, target_identifier: '10.0.0.0/26', status: 'running', current_stage: 'httpx', assets_found: 0, hosts_scanned: 0 },
        { id: 'c1', scan_id: 'scan-1', chunk_index: 1, target_identifier: '10.0.0.64/26', status: 'pending', assets_found: 0, hosts_scanned: 0 },
      ],
    });
    const events: ScanProgressPayload[] = [
      {
        scan_id: 'scan-1', event: 'chunk_completed', status: 'running',
        chunks_total: 2, chunks_completed: 1, chunks_failed: 0,
        chunk: { chunk_id: 'c0', chunk_index: 0, status: 'completed', hosts_scanned: 12, assets_found: 3 },
      },
    ];
    const v = mergeProgress(data, events)!;
    expect(v.hasChunkModel).toBe(true);
    expect(v.chunksTotal).toBe(2);
    expect(v.chunksCompleted).toBe(1);
    const c0 = v.chunks.find((c) => c.chunkIndex === 0)!;
    expect(c0.status).toBe('completed');
    expect(c0.hostsScanned).toBe(12);
    expect(c0.assetsFound).toBe(3);
    // untouched chunk keeps its seeded state
    expect(v.chunks.find((c) => c.chunkIndex === 1)!.status).toBe('pending');
  });

  it('applies stage_progress current_stage on the keyed chunk', () => {
    const data = detail({
      scan_type: 'discovery', chunks_total: 1,
      chunks: [{ id: 'c0', scan_id: 'scan-1', chunk_index: 0, target_identifier: '10.0.0.0/24', status: 'running', current_stage: 'naabu', assets_found: 0, hosts_scanned: 0 }],
    });
    const events: ScanProgressPayload[] = [
      { scan_id: 'scan-1', event: 'stage_progress', status: 'running', chunks_total: 1, chunks_completed: 0, chunks_failed: 0, chunk: { chunk_id: 'c0', status: 'running', current_stage: 'nuclei' } },
    ];
    expect(mergeProgress(data, events)!.chunks[0].currentStage).toBe('nuclei');
  });

  it('compliance has no chunk model (total=0, no chunks)', () => {
    const v = mergeProgress(detail({ scan_type: 'compliance', chunks_total: 0 }), [])!;
    expect(v.hasChunkModel).toBe(false);
    expect(v.chunks).toHaveLength(0);
  });

  it('non-chunked discovery synthesizes one implicit chunk', () => {
    const v = mergeProgress(detail({ scan_type: 'discovery', chunks_total: 1, chunks: [] }), [])!;
    expect(v.hasChunkModel).toBe(true);
    expect(v.chunks).toHaveLength(1);
    expect(v.chunks[0].chunkIndex).toBe(0);
  });

  // hero Finding 1: a terminal REST snapshot must not be reverted to running by
  // older buffered non-terminal events (the missed-final-event reconnect case).
  it('terminal REST snapshot ignores stale buffered running events', () => {
    const data = detail({
      scan_type: 'discovery', status: 'completed',
      chunks_total: 1, chunks_completed: 1,
      chunks: [{ id: 'c0', scan_id: 'scan-1', chunk_index: 0, target_identifier: '10.0.0.0/24', status: 'completed', current_stage: 'nuclei', assets_found: 5, hosts_scanned: 9 }],
    });
    // A stale stage_progress that arrived before the REST refetch.
    const events: ScanProgressPayload[] = [
      { scan_id: 'scan-1', event: 'stage_progress', status: 'running', chunks_total: 1, chunks_completed: 0, chunks_failed: 0, chunk: { chunk_id: 'c0', status: 'running', current_stage: 'httpx' } },
    ];
    const v = mergeProgress(data, events)!;
    expect(v.status).toBe('completed');
    expect(v.chunksCompleted).toBe(1);
    expect(v.chunks[0].status).toBe('completed'); // not reverted to running
  });

  // hero Finding 2: a scan-level terminal event carries no chunk, so the
  // implicit non-chunked unit must still settle to the final status.
  it('non-chunked discovery settles the implicit chunk on scan_completed', () => {
    const data = detail({ scan_type: 'discovery', status: 'running', chunks_total: 1, chunks: [] });
    const events: ScanProgressPayload[] = [
      { scan_id: 'scan-1', event: 'stage_progress', status: 'running', chunks_total: 1, chunks_completed: 0, chunks_failed: 0, chunk: { chunk_index: 0, status: 'running', current_stage: 'nuclei' } },
      { scan_id: 'scan-1', event: 'scan_completed', status: 'completed', chunks_total: 1, chunks_completed: 1, chunks_failed: 0 },
    ];
    const v = mergeProgress(data, events)!;
    expect(v.status).toBe('completed');
    expect(v.chunks).toHaveLength(1);
    expect(v.chunks[0].status).toBe('completed'); // not stuck running
  });

  // hero ordering case: when the REST snapshot is stale-running (not terminal),
  // fresher buffered events DO win — the terminal guard only protects a REST
  // snapshot that is already terminal.
  it('stale running REST snapshot accepts fresher buffered terminal events', () => {
    const data = detail({
      scan_type: 'discovery', status: 'running',
      chunks_total: 2, chunks_completed: 0,
      chunks: [
        { id: 'c0', scan_id: 'scan-1', chunk_index: 0, target_identifier: '10.0.0.0/26', status: 'running', current_stage: 'nuclei', assets_found: 0, hosts_scanned: 0 },
        { id: 'c1', scan_id: 'scan-1', chunk_index: 1, target_identifier: '10.0.0.64/26', status: 'running', current_stage: 'httpx', assets_found: 0, hosts_scanned: 0 },
      ],
    });
    const events: ScanProgressPayload[] = [
      { scan_id: 'scan-1', event: 'chunk_completed', status: 'running', chunks_total: 2, chunks_completed: 1, chunks_failed: 0, chunk: { chunk_id: 'c0', status: 'completed', hosts_scanned: 4, assets_found: 1 } },
      { scan_id: 'scan-1', event: 'chunk_completed', status: 'running', chunks_total: 2, chunks_completed: 2, chunks_failed: 0, chunk: { chunk_id: 'c1', status: 'completed', hosts_scanned: 6, assets_found: 2 } },
      { scan_id: 'scan-1', event: 'scan_completed', status: 'completed', chunks_total: 2, chunks_completed: 2, chunks_failed: 0 },
    ];
    const v = mergeProgress(data, events)!;
    expect(v.status).toBe('completed');
    expect(v.chunksCompleted).toBe(2);
    expect(v.chunks.every((c) => c.status === 'completed')).toBe(true);
  });

  it('returns null without data', () => {
    expect(mergeProgress(undefined, [])).toBeNull();
  });
});

describe('stageState', () => {
  it('marks earlier stages done, the current stage active, later pending while running', () => {
    const c = { key: 'c', status: 'running', currentStage: 'httpx' };
    expect(stageState('naabu', c)).toBe('done');
    expect(stageState('httpx', c)).toBe('active');
    expect(stageState('nuclei', c)).toBe('pending');
  });

  it('marks all stages done on a completed chunk', () => {
    const c = { key: 'c', status: 'completed', currentStage: 'httpx' };
    expect(stageState('nuclei', c)).toBe('done');
  });

  it('is all pending before any stage is reported', () => {
    const c = { key: 'c', status: 'running' };
    expect(stageState('naabu', c)).toBe('pending');
  });
});
