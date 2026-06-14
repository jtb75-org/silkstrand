import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createInstallToken, previewAllowlist, listAgents,
  type AllowlistPreview,
} from '../api/client';
import type { Agent } from '../api/types';
import CodeBlock from './CodeBlock';

// Single-quote a user-supplied value so it is safe to paste into a shell: an
// allow-target can be a wildcard hostname (*.example.com) which would otherwise
// glob, and free-form input must never be interpreted (spaces, ;, `, etc.).
const shQuote = (s: string) => `'${s.replace(/'/g, "'\\''")}'`;

// The Helm chart's allowlist.cidrs is CIDR-only and is also rendered into a
// NetworkPolicy ipBlock.cidr, so the Helm tab must filter the shared allow input
// down to valid IPv4 CIDRs (bare IPs, ranges, and hostnames are Linux/Docker-only
// until the chart grows an allowlist.allow field). Valid CIDRs are digits/dots/
// slash only, so they can never break the `--set {…}` list expression either.
const isIpv4Cidr = (s: string) => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(s.trim());
  if (!m) return false;
  if ([m[1], m[2], m[3], m[4]].some((o) => Number(o) > 255)) return false;
  return Number(m[5]) <= 32;
};

type Method = 'linux' | 'docker' | 'helm';

const METHODS: { value: Method; label: string; sub: string }[] = [
  { value: 'linux', label: 'Linux', sub: 'binary' },
  { value: 'docker', label: 'Docker', sub: 'container' },
  { value: 'helm', label: 'Kubernetes', sub: 'Helm' },
];

// A token request carries an immutable snapshot of the config it was minted for,
// so concurrent edits while the request is in flight can't desync the staleness
// signature or the verify-on-connect baseline (hero review #2/#3).
interface TokenSnapshot {
  zone?: string;
  autoDiscover: boolean;
  discoverSchedule: 'off' | 'daily' | 'weekly';
  sig: string;
  baselineIds: Set<string>;
  mintedAt: number;
}

// The just-deployed agent = the first NOT in the mint-time baseline that comes up
// connected, created at/after mint (− skew tolerance). Pure so both the render
// memo and the poll-interval predicate share one definition.
function matchNewAgent(list: Agent[] | undefined, seen: Set<string> | null, mintedAt: number): Agent | null {
  if (!list || !seen) return null;
  return list.find(
    (a) => !seen.has(a.id) &&
      (a.status === 'connected' || a.status === 'online') &&
      new Date(a.created_at).getTime() >= mintedAt - 120_000,
  ) ?? null;
}

interface Props {
  apiURL: string;
  installScriptURL: string;
  binaries?: Record<string, string>;
  onClose: () => void;
}

// AddAgentModal — onboarding for a NEW agent, split from the management list
// (#386). The install token is minted ONCE in the shared config header; the
// method tabs only show how to consume the same token + API URL + allowlist.
export default function AddAgentModal({ apiURL, installScriptURL, binaries, onClose }: Props) {
  const qc = useQueryClient();

  // Esc / backdrop dismiss — design-system §5.9 (non-destructive modal).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  // --- shared config (token metadata + command args) -----------------------
  const [zone, setZone] = useState('');
  const [allowCidrs, setAllowCidrs] = useState<string[]>(['']);
  const [autoDiscover, setAutoDiscover] = useState(true);
  const [discoverSchedule, setDiscoverSchedule] = useState<'off' | 'daily' | 'weekly'>('off');
  const [proxy, setProxy] = useState('');
  const [noProxy, setNoProxy] = useState('');
  const [caCert, setCaCert] = useState('');

  const addAllow = () => setAllowCidrs((a) => [...a, '']);
  const updateAllow = (i: number, v: string) =>
    setAllowCidrs((a) => a.map((x, j) => (j === i ? v : x)));
  const removeAllow = (i: number) => setAllowCidrs((a) => a.filter((_, j) => j !== i));
  const cleanAllow = allowCidrs.map((c) => c.trim()).filter(Boolean);

  const [method, setMethod] = useState<Method>('linux');
  const [installToken, setInstallToken] = useState<{ token: string; expiresAt: string; sig: string; mintedAt: number } | null>(null);
  const [overlapPreview, setOverlapPreview] = useState<AllowlistPreview | null>(null);
  // Snapshot awaiting operator confirmation in the overlap modal.
  const [pendingSnap, setPendingSnap] = useState<TokenSnapshot | null>(null);
  // Agent ids present when the current token was minted (verify-on-connect).
  const [seenIds, setSeenIds] = useState<Set<string> | null>(null);
  const [preparing, setPreparing] = useState(false);

  // hero refinement #1: zone / auto-discover / recurrence are token METADATA —
  // changing them after minting makes the shown token stale. Derive staleness
  // from the signature stamped at mint time and prompt Regenerate (no effect).
  // (Allowlist / proxy / CA are command args; they re-render live, no re-mint.)
  const metaSig = `${zone.trim()}|${autoDiscover}|${discoverSchedule}`;
  const stale = !!installToken && installToken.sig !== metaSig;

  const tokenMutation = useMutation({
    mutationFn: (snap: TokenSnapshot) =>
      createInstallToken({
        auto_discover: snap.autoDiscover,
        discover_schedule: snap.discoverSchedule,
        zone: snap.zone,
      }),
    // Stamp the token from the SNAPSHOT, not live state, so an edit mid-request
    // can't mismark the result stale or seed a wrong verify baseline.
    onSuccess: (res, snap) => {
      setOverlapPreview(null);
      setPendingSnap(null);
      setSeenIds(snap.baselineIds);
      setInstallToken({ token: res.install_token, expiresAt: res.expires_at, sig: snap.sig, mintedAt: snap.mintedAt });
    },
  });

  // Generate runs the overlap preview first; only non-empty allowlists trigger it.
  const previewMutation = useMutation({
    mutationFn: (snap: TokenSnapshot) => previewAllowlist(cleanAllow, snap.zone).then((res) => ({ res, snap })),
    onSuccess: ({ res, snap }) => {
      if (res.overlaps.length > 0 || res.redundant.length > 0) {
        setPendingSnap(snap);     // carry the snapshot through to "Proceed anyway"
        setOverlapPreview(res);   // surface confirm modal; operator proceeds
      } else {
        tokenMutation.mutate(snap); // clean — mint straight away
      }
    },
  });

  const handleGenerate = async () => {
    setPreparing(true);
    setInstallToken(null);
    setOverlapPreview(null);
    setPendingSnap(null);
    try {
      // Guarantee a real verify-on-connect baseline (hero #2): if the agents
      // query hasn't resolved yet, an empty snapshot would treat a pre-existing
      // connected agent as "just installed". fetchQuery resolves from cache or
      // network before we snapshot.
      const baseline = await qc.fetchQuery({ queryKey: ['agents'], queryFn: listAgents });
      const snap: TokenSnapshot = {
        zone: zone.trim() || undefined,
        autoDiscover,
        discoverSchedule,
        sig: metaSig,
        baselineIds: new Set(baseline.map((a) => a.id)),
        mintedAt: Date.now(),
      };
      if (cleanAllow.length === 0) tokenMutation.mutate(snap);
      else previewMutation.mutate(snap);
    } finally {
      setPreparing(false);
    }
  };

  const generating = preparing || tokenMutation.isPending || previewMutation.isPending;

  // --- verify-on-connect ---------------------------------------------------
  // The first agent NOT in the mint-time baseline that comes up connected is the
  // one the user just deployed. Critical for the Helm tab, where a pull-secret/
  // PVC/NetworkPolicy mistake otherwise dumps the user into kubectl with no signal.
  const { data: agents } = useQuery<Agent[]>({
    queryKey: ['agents'],
    queryFn: listAgents,
    // Poll while waiting for the just-deployed agent; stop once it's found.
    refetchInterval: (q) =>
      installToken && !matchNewAgent(q.state.data, seenIds, installToken.mintedAt) ? 4000 : false,
  });

  const connectedAgent = useMemo(
    () => (installToken ? matchNewAgent(agents, seenIds, installToken.mintedAt) : null),
    [installToken, seenIds, agents],
  );

  // --- command builders ----------------------------------------------------
  // install.sh bootstraps over HTTP(S); the Helm chart / agent env want WSS.
  // Normalize from whatever scheme active.dc_api_url carries so neither breaks.
  const httpApiURL = apiURL.replace(/^ws/, 'http'); // ws→http, wss→https
  const wsURL = apiURL.replace(/^http/, 'ws');       // http→ws, https→wss
  const advFlags = [
    ...(proxy.trim() ? [`--proxy=${shQuote(proxy.trim())}`] : []),
    ...(noProxy.trim() ? [`--no-proxy=${shQuote(noProxy.trim())}`] : []),
    ...(caCert.trim() ? [`--ca-cert=${shQuote(caCert.trim())}`] : []),
  ];
  const allowFlags = cleanAllow.map((c) => `--allow-cidr=${shQuote(c)}`);

  const linuxCmd = installToken && apiURL && installScriptURL
    ? `curl -sSL ${shQuote(installScriptURL)} | sudo sh -s -- \\\n  ${[
        `--token=${installToken.token}`,
        `--api-url=${shQuote(httpApiURL)}`,
        `--name=$(hostname)`,
        ...allowFlags, ...advFlags,
        `--as-service`,
      ].join(' \\\n  ')}`
    : '';

  const dockerCmd = installToken && apiURL && installScriptURL
    ? `curl -sSL ${shQuote(installScriptURL)} | sudo sh -s -- \\\n  ${[
        `--mode=docker`,
        `--token=${installToken.token}`,
        `--api-url=${shQuote(httpApiURL)}`,
        `--name=$(hostname)-docker`,
        ...allowFlags, ...advFlags,
      ].join(' \\\n  ')}`
    : '';

  // Helm: only valid CIDRs feed allowlist.cidrs (chart is CIDR-only + drives a
  // NetworkPolicy). Non-CIDR allow entries are surfaced as a warning in the tab.
  const helmCidrAllow = cleanAllow.filter(isIpv4Cidr);
  const helmNonCidrAllow = cleanAllow.filter((c) => !isIpv4Cidr(c));
  const helmCidrSet = helmCidrAllow.length
    ? `\\\n  --set 'allowlist.cidrs={${helmCidrAllow.join(',')}}'`
    : '';
  const helmCmd = installToken
    ? `helm install silkstrand-agent oci://zot.ng20.org/charts/silkstrand-agent \\\n  --namespace silkstrand-agent --create-namespace \\\n  --set auth.installToken=${installToken.token} \\\n  --set apiUrl=${wsURL}${helmCidrSet} \\\n  --set 'imagePullSecrets[0].name=zot-pull'`
    : '';

  const ready = !!apiURL && !!installScriptURL;

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="form-card"
        style={{ maxWidth: 760, width: 'calc(100vw - 48px)', maxHeight: 'calc(100vh - 64px)', overflowY: 'auto' }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label="Add agent"
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
          <h2 style={{ marginTop: 0 }}>Add Agent</h2>
          <button type="button" className="btn btn-sm" onClick={onClose} aria-label="Close">×</button>
        </div>
        <p className="muted" style={{ marginTop: 0 }}>
          Configure the agent, generate a one-time install token, then run it with
          your platform of choice. The agent registers itself automatically.
        </p>

        {/* ----- shared config header ----- */}
        <div style={{ margin: '8px 0 16px' }}>
          <label style={{ fontWeight: 600, fontSize: 14 }} htmlFor="agent-zone">
            Zone / site <span className="muted" style={{ fontWeight: 400 }}>(optional)</span>
          </label>
          <input
            id="agent-zone"
            type="text"
            placeholder="office-east"
            value={zone}
            onChange={(e) => setZone(e.target.value)}
            style={{ display: 'block', marginTop: 6, maxWidth: 320 }}
          />
          <p className="muted" style={{ fontSize: 13, marginTop: 6 }}>
            Disambiguates the same private ranges reused at different sites, so the
            overlap check doesn't false-alarm. Stored server-side, not in the command.
          </p>
        </div>

        <div style={{ margin: '12px 0 16px' }}>
          <label style={{ fontWeight: 600, fontSize: 14 }}>
            Allowed targets{' '}
            <span className="muted" style={{ fontWeight: 400 }}>(the agent only ever scans these)</span>
          </label>
          {allowCidrs.map((c, i) => (
            <div key={i} style={{ display: 'flex', gap: 8, marginTop: 6, alignItems: 'center' }}>
              <input
                type="text"
                placeholder="192.168.0.0/24 · 10.0.0.5 · 10.0.0.10-10.0.0.50 · host.example.com"
                value={c}
                onChange={(e) => updateAllow(i, e.target.value)}
                style={{ flex: '1 1 auto', minWidth: 0 }}
              />
              {allowCidrs.length > 1 && (
                <button type="button" className="btn btn-sm" onClick={() => removeAllow(i)} aria-label="Remove target" style={{ flex: '0 0 auto' }}>×</button>
              )}
            </div>
          ))}
          <button type="button" className="btn btn-sm" onClick={addAllow} style={{ marginTop: 8 }}>+ Add target</button>
          <p className="muted" style={{ fontSize: 13, marginTop: 6 }}>
            Seeds the agent's scan allowlist (CIDR, IP, range, or hostname). You can
            edit it later on the host or via the <strong>Allowlist</strong> row action.
            Leave empty to configure it on the host instead.
          </p>
        </div>

        <div style={{ margin: '12px 0 16px' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 14 }}>
            <input type="checkbox" checked={autoDiscover} onChange={(e) => setAutoDiscover(e.target.checked)} />
            <span style={{ fontWeight: 600 }}>Run discovery as soon as it connects</span>
          </label>
          {autoDiscover && (
            <div style={{ marginTop: 6, marginLeft: 24, display: 'flex', alignItems: 'center', gap: 8 }}>
              <span className="muted" style={{ fontSize: 13 }}>then repeat:</span>
              {(['off', 'daily', 'weekly'] as const).map((opt) => (
                <label key={opt} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 13 }}>
                  <input type="radio" name="discover-schedule" checked={discoverSchedule === opt} onChange={() => setDiscoverSchedule(opt)} />
                  {opt === 'off' ? 'Off' : opt === 'daily' ? 'Daily' : 'Weekly'}
                </label>
              ))}
            </div>
          )}
        </div>

        <details style={{ margin: '12px 0 16px' }}>
          <summary style={{ cursor: 'pointer', fontSize: 14, fontWeight: 600 }}>Advanced — proxy &amp; custom CA</summary>
          <div style={{ marginTop: 10, display: 'grid', gap: 10, maxWidth: 440 }}>
            <label style={{ fontSize: 13 }}>HTTPS proxy
              <input type="text" placeholder="http://proxy.corp:3128" value={proxy} onChange={(e) => setProxy(e.target.value)} style={{ display: 'block', marginTop: 4, width: '100%' }} />
            </label>
            <label style={{ fontSize: 13 }}>No-proxy list
              <input type="text" placeholder="10.0.0.0/8,.internal" value={noProxy} onChange={(e) => setNoProxy(e.target.value)} style={{ display: 'block', marginTop: 4, width: '100%' }} />
            </label>
            <label style={{ fontSize: 13 }}>Custom CA cert (path on the host)
              <input type="text" placeholder="/etc/ssl/certs/corp-root.pem" value={caCert} onChange={(e) => setCaCert(e.target.value)} style={{ display: 'block', marginTop: 4, width: '100%' }} />
            </label>
            <p className="muted" style={{ fontSize: 12, margin: 0 }}>
              For TLS-inspecting proxies (Linux &amp; Docker). The CA file must already
              exist on the host — its path is passed, never its contents. No proxy
              credentials go in the command. For Helm, set these via chart values.
            </p>
          </div>
        </details>

        {/* ----- generate ----- */}
        <button className="btn btn-primary" disabled={generating || !ready} onClick={() => void handleGenerate()}>
          {generating ? 'Generating…' : installToken ? 'Regenerate token' : 'Generate install token'}
        </button>
        {!ready && <p className="muted" style={{ fontSize: 13 }}>Loading the agent release location…</p>}
        {tokenMutation.error && <p className="error">{(tokenMutation.error as Error).message}</p>}
        {previewMutation.error && <p className="error">{(previewMutation.error as Error).message}</p>}

        {/* ----- token metadata changed → prompt regenerate ----- */}
        {installToken && stale && (
          <p style={{ marginTop: 16, fontSize: 13, color: '#92400e' }}>
            ⚠ Zone / discovery settings changed since this token was generated.
            Click <strong>Regenerate token</strong> to refresh it.
          </p>
        )}

        {/* ----- token + method tabs ----- */}
        {installToken && !stale && (
          <>
            <p className="muted" style={{ marginTop: 16, fontSize: 13 }}>
              <strong>Single-use token</strong> — use it with one install method below.
              Expires {new Date(installToken.expiresAt).toLocaleString()}. Changed a
              setting? Click <em>Regenerate</em>. Need another agent? Generate a new one.
            </p>

            <div className="tab-bar" style={{ marginTop: 8 }}>
              {METHODS.map((m) => (
                <button
                  key={m.value}
                  type="button"
                  className={`tab ${method === m.value ? 'tab-active' : ''}`}
                  onClick={() => setMethod(m.value)}
                >
                  {m.label} <span className="muted" style={{ fontSize: 12 }}>({m.sub})</span>
                </button>
              ))}
            </div>

            <div style={{ marginTop: 12 }}>
              {method === 'linux' && (
                <>
                  <p className="muted" style={{ marginTop: 0, fontSize: 13 }}>
                    Run on the host (requires sudo). Installs the binary as a system
                    service. Reports <code>in_container=false</code>.
                  </p>
                  <CodeBlock content={linuxCmd} />
                  <p className="muted" style={{ fontSize: 13 }}>
                    Drop <code>--as-service</code> to install the binary + credentials
                    only (you run silkstrand-agent yourself).
                  </p>
                  {binaries && Object.keys(binaries).length > 0 && (
                    <details style={{ marginTop: 8 }}>
                      <summary style={{ cursor: 'pointer', fontSize: 13 }}>Download a binary directly</summary>
                      <ul style={{ margin: '8px 0 0', paddingLeft: 20 }}>
                        {Object.entries(binaries).map(([platform, url]) => (
                          <li key={platform}><a href={url}>{platform}</a></li>
                        ))}
                      </ul>
                    </details>
                  )}
                </>
              )}

              {method === 'docker' && (
                <>
                  <p className="muted" style={{ marginTop: 0, fontSize: 13 }}>
                    Requires Docker on the host. The installer pulls the agent image,
                    seeds the allowlist, and runs it with <code>--restart unless-stopped</code>.
                  </p>
                  <CodeBlock content={dockerCmd} />
                  <p className="muted" style={{ fontSize: 13 }}>
                    If the agent registry requires authentication, run{' '}
                    <code>docker login zot.ng20.org</code> first (image-pull auth — #369).
                  </p>
                </>
              )}

              {method === 'helm' && (
                <>
                  <div className="badge" style={{ background: '#fef3c7', color: '#92400e', fontSize: 12 }}>Preview</div>
                  <p className="muted" style={{ margin: '8px 0 0', fontSize: 13 }}>
                    Deploys the agent as a Kubernetes Deployment with a persistent
                    identity (resume across restarts) and a scoped egress NetworkPolicy.
                  </p>
                  <ol className="muted" style={{ fontSize: 13, paddingLeft: 18, margin: '8px 0' }}>
                    <li>Create an image-pull secret in the target namespace (registry auth — #369):
                      <CodeBlock content={`kubectl create namespace silkstrand-agent\nkubectl -n silkstrand-agent create secret docker-registry zot-pull \\\n  --docker-server=zot.ng20.org \\\n  --docker-username=<user> --docker-password=<token>`} />
                    </li>
                    <li style={{ marginTop: 8 }}>Install the chart:</li>
                  </ol>
                  <CodeBlock content={helmCmd} />
                  {helmNonCidrAllow.length > 0 && (
                    <p style={{ fontSize: 13, color: '#92400e', marginTop: 8 }}>
                      ⚠ The chart's allowlist is <strong>CIDR-only</strong>, so{' '}
                      {helmNonCidrAllow.map((c) => <code key={c} style={{ marginRight: 4 }}>{c}</code>)}
                      {helmNonCidrAllow.length === 1 ? ' is' : ' are'} omitted from the command.
                      Add {helmNonCidrAllow.length === 1 ? 'it' : 'them'} to the agent's
                      allowlist file after install, or use the Linux / Docker method
                      (which accept IPs, ranges, and hostnames).
                    </p>
                  )}
                  {helmCidrAllow.length === 0 && (
                    <p className="muted" style={{ fontSize: 13, marginTop: 8 }}>
                      No CIDR targets — the command omits <code>allowlist.cidrs</code>, so the
                      chart default applies. Set it explicitly for anything but a quick trial.
                    </p>
                  )}
                  <p className="muted" style={{ fontSize: 13 }}>
                    Keep <code>replicaCount</code> at 1 (single identity + RWO creds).
                    Proxy / custom-CA clusters set <code>networkPolicy.extraEgress</code>{' '}
                    and <code>extraEnv</code> — see the chart README. Chart publishing is
                    being finalized (#388).
                  </p>
                </>
              )}
            </div>

            {/* ----- verify-on-connect ----- */}
            <div style={{ marginTop: 16, padding: 12, borderRadius: 6, background: connectedAgent ? '#ecfdf5' : '#f3f4f6', border: `1px solid ${connectedAgent ? '#a7f3d0' : '#e5e7eb'}` }}>
              {connectedAgent ? (
                <span style={{ fontSize: 14, color: '#065f46' }}>
                  ✓ <strong>{connectedAgent.name}</strong> connected
                  {connectedAgent.zone ? ` · zone ${connectedAgent.zone}` : ''}
                  {connectedAgent.version ? ` · ${connectedAgent.version}` : ''}.
                  {autoDiscover ? ' Discovery queued.' : ''}
                </span>
              ) : (
                <span style={{ fontSize: 14, color: '#374151', display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ width: 8, height: 8, borderRadius: '50%', background: '#9ca3af', animation: 'pulse 1.5s ease-in-out infinite' }} />
                  Waiting for the agent to connect…
                </span>
              )}
            </div>
          </>
        )}

        {/* ----- overlap confirm ----- */}
        {overlapPreview && (
          <div className="modal-backdrop" onClick={() => { setOverlapPreview(null); setPendingSnap(null); }}>
            <div className="form-card" style={{ maxWidth: 560 }} onClick={(e) => e.stopPropagation()}>
              <h3 style={{ marginTop: 0 }}>⚠️ These ranges may already be scanned</h3>
              {overlapPreview.overlaps.length > 0 && (
                <>
                  <p style={{ marginBottom: 8 }}>
                    Another agent already discovers across overlapping ranges. If they
                    see the <strong>same network</strong>, you'll scan these hosts twice.
                  </p>
                  <ul style={{ margin: '0 0 12px', paddingLeft: 18 }}>
                    {overlapPreview.overlaps.map((o, i) => (
                      <li key={i} style={{ marginBottom: 4 }}>
                        <code>{o.cidr}</code> overlaps agent{' '}
                        <strong>{o.conflicts_with.name || o.conflicts_with.id}</strong>{' '}
                        (allowlist <code>{o.conflicts_with.range}</code>
                        {o.conflicts_with.zone ? `, zone ${o.conflicts_with.zone}` : ''})
                      </li>
                    ))}
                  </ul>
                </>
              )}
              {overlapPreview.redundant.length > 0 && (
                <>
                  <p style={{ marginBottom: 8 }}>Some entries are redundant:</p>
                  <ul style={{ margin: '0 0 12px', paddingLeft: 18 }}>
                    {overlapPreview.redundant.map((r, i) => (<li key={i} style={{ marginBottom: 4 }}>{r}</li>))}
                  </ul>
                </>
              )}
              <p className="muted" style={{ fontSize: 13 }}>
                Overlap is a hint, not a rule — the same private range at two sites is
                fine. If these are different private sites, set distinct zones to
                suppress this warning, or proceed if it's intentional.
              </p>
              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
                <button className="btn" onClick={() => { setOverlapPreview(null); setPendingSnap(null); }} disabled={tokenMutation.isPending}>Adjust ranges</button>
                <button
                  className="btn btn-primary"
                  disabled={tokenMutation.isPending || !pendingSnap}
                  onClick={() => { if (pendingSnap) tokenMutation.mutate(pendingSnap); }}
                >
                  {tokenMutation.isPending ? 'Generating…' : 'Proceed anyway'}
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
