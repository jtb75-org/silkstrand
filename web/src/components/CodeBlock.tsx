import { useState } from 'react';

// CodeBlock — monospace box with a Copy button in the corner.
// Falls back to manual-select when navigator.clipboard isn't available
// (old browsers, non-HTTPS contexts).
export default function CodeBlock({ content }: { content: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(content);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Ignore — user can still select manually thanks to userSelect: all.
    }
  }
  return (
    <div style={{ position: 'relative' }}>
      <pre
        style={{
          background: '#111', color: '#eee', padding: 12, paddingRight: 64,
          borderRadius: 6, overflowX: 'auto', userSelect: 'all',
          margin: 0,
        }}
      >{content}</pre>
      <button
        type="button"
        onClick={copy}
        className="btn btn-sm"
        style={{
          position: 'absolute', top: 6, right: 6,
          background: '#222', color: '#eee', borderColor: '#333',
        }}
      >
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}
