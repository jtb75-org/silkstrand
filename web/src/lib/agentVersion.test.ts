import { describe, it, expect } from 'vitest';
import { normalizeVersion, isUpdateAvailable } from './agentVersion';

describe('normalizeVersion', () => {
  it('strips a single leading v (any case)', () => {
    expect(normalizeVersion('v0.1.52')).toBe('0.1.52');
    expect(normalizeVersion('V0.1.52')).toBe('0.1.52');
    expect(normalizeVersion('0.1.52')).toBe('0.1.52');
  });
});

describe('isUpdateAvailable', () => {
  it('up-to-date binary agent (no v) vs v-prefixed tag → no hint', () => {
    expect(isUpdateAvailable('0.1.52', 'v0.1.52', 'connected')).toBe(false);
  });
  it('up-to-date container agent (v-prefixed) vs v-prefixed tag → no hint', () => {
    expect(isUpdateAvailable('v0.1.52', 'v0.1.52', 'connected')).toBe(false);
  });
  it('outdated binary agent → hint', () => {
    expect(isUpdateAvailable('0.1.51', 'v0.1.52', 'connected')).toBe(true);
  });
  it('outdated container agent → hint', () => {
    expect(isUpdateAvailable('v0.1.51', 'v0.1.52', 'connected')).toBe(true);
  });
  it('disconnected agent → no hint', () => {
    expect(isUpdateAvailable('0.1.51', 'v0.1.52', 'disconnected')).toBe(false);
  });
  it('dev / unset agent version → no hint', () => {
    expect(isUpdateAvailable('dev', 'v0.1.52', 'connected')).toBe(false);
    expect(isUpdateAvailable(undefined, 'v0.1.52', 'connected')).toBe(false);
  });
  it('latest fallback / missing manifest → no hint', () => {
    expect(isUpdateAvailable('0.1.51', 'latest', 'connected')).toBe(false);
    expect(isUpdateAvailable('0.1.51', undefined, 'connected')).toBe(false);
  });
});
