import { useEffect, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { getScanDetail } from '../api/client';
import { useEventStream } from '../lib/events';
import type { ScanDetail, ScanProgressPayload } from '../api/types';
import {
  STAGES,
  mergeProgress,
  stageState,
  type ChunkView,
  type ProgressView,
} from '../lib/scanProgress';

// #387: live Scan Activity detail. Seeds from GET /scans/{id} (ScanDetail),
// then folds scan_progress SSE events on top so overall + per-chunk +
// per-stage progress update in real time. One subscription (kind=scan_progress,
// scan_id); reconnect/token-refresh are handled by useEventStream. The merge
// logic lives in ../lib/scanProgress (unit-tested separately).

function Bar({ value, total, failed }: { value: number; total: number; failed?: boolean }) {
  const pct = total > 0 ? Math.round((value / total) * 100) : 0;
  return (
    <div className="ss-progress" aria-hidden>
      <div className={`ss-progress-fill${failed ? ' failed' : ''}`} style={{ width: `${pct}%` }} />
    </div>
  );
}

function ChunkRow({ chunk }: { chunk: ChunkView }) {
  return (
    <div className="ss-chunk">
      <div className="ss-chunk-head">
        <strong>
          Chunk {chunk.chunkIndex ?? 0}
          {chunk.targetIdentifier ? ` · ${chunk.targetIdentifier}` : ''}
        </strong>
        <span className={`badge badge-${chunk.status}`}>{chunk.status}</span>
      </div>
      <div className="ss-stage-row">
        {STAGES.map((s) => (
          <span key={s} className={`ss-stage ${stageState(s, chunk)}`}>{s}</span>
        ))}
      </div>
      <div className="muted" style={{ marginTop: 6 }}>
        {chunk.hostsScanned != null ? `${chunk.hostsScanned} hosts` : '— hosts'}
        {' · '}
        {chunk.assetsFound != null ? `${chunk.assetsFound} assets` : '— assets'}
      </div>
      {chunk.errorMessage && <div className="error" style={{ marginTop: 4 }}>{chunk.errorMessage}</div>}
    </div>
  );
}

function ProgressBody({
  view,
  streamStatus,
  scanId,
}: {
  view: ProgressView;
  streamStatus: string;
  scanId: string;
}) {
  return (
    <>
      <section>
        <div className="ss-chunk-head" style={{ marginBottom: 8 }}>
          <span className={`badge badge-${view.status}`}>{view.status}</span>
          <span className={`ss-stream-pill badge-${streamStatus === 'connected' ? 'completed' : 'pending'}`}>
            {streamStatus === 'connected' ? 'live' : streamStatus}
          </span>
        </div>
        {view.hasChunkModel ? (
          <>
            <Bar value={view.chunksCompleted} total={view.chunksTotal} failed={view.chunksFailed > 0} />
            <div className="muted" style={{ marginTop: 6 }}>
              {view.chunksCompleted} / {view.chunksTotal} chunks
              {view.chunksFailed > 0 ? ` · ${view.chunksFailed} failed` : ''}
            </div>
          </>
        ) : (
          <p className="muted" style={{ margin: 0 }}>No chunk breakdown — compliance scan.</p>
        )}
      </section>

      {view.chunks.length > 0 && (
        <section>
          <h3>Chunks</h3>
          {view.chunks.map((c) => <ChunkRow key={c.key} chunk={c} />)}
        </section>
      )}

      <section>
        <Link to={`/scans/${scanId}`}>Open full results ↗</Link>
      </section>
    </>
  );
}

interface Props {
  scanId: string;
  onClose: () => void;
}

export default function ScanActivityDrawer({ scanId, onClose }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const { data, isLoading, error } = useQuery({
    queryKey: ['scan-detail', scanId],
    queryFn: () => getScanDetail(scanId),
    enabled: !!scanId,
    // SSE drives live updates; this is the reconnect/refresh fallback for the
    // snapshot while the scan is active.
    refetchInterval: (query) => {
      const d = query.state.data as ScanDetail | undefined;
      return d && (d.status === 'running' || d.status === 'pending' || d.status === 'queued')
        ? 15000
        : false;
    },
  });

  const { events, status: streamStatus } = useEventStream<ScanProgressPayload>(
    { kinds: ['scan_progress'], scan_id: scanId },
    { enabled: !!scanId },
  );

  const view = useMemo(() => {
    const payloads = events
      .map((e) => e.payload)
      .filter((p): p is ScanProgressPayload => !!p);
    return mergeProgress(data, payloads);
  }, [data, events]);

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer drawer-wide" role="dialog" aria-label="Scan progress">
        <header className="drawer-header">
          <h2>Scan progress</h2>
          <button type="button" className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <div className="drawer-body">
          {isLoading && <p className="muted">Loading…</p>}
          {error && <p className="error">{(error as Error).message}</p>}
          {view && <ProgressBody view={view} streamStatus={streamStatus} scanId={scanId} />}
        </div>
      </aside>
    </>
  );
}
