// Agent version helpers for the "update available" hint.
//
// Binary agents report main.version without a leading "v" (built from
// ${GITHUB_REF#refs/tags/v}, e.g. "0.1.52"); container agents report it WITH the
// "v" (the image build passes --build-arg VERSION="v${VERSION}"). The release
// manifest (downloads.version) is the v-prefixed release tag (e.g. "v0.1.52")
// because the installer/recreate --version needs that exact folder/image tag.
// So the update comparison must strip a single leading "v" on both sides.

/** Strip a single leading "v"/"V" so binary, container, and tag forms compare equal. */
export function normalizeVersion(v: string): string {
  return v.replace(/^v/i, '');
}

/**
 * Whether to show the "update available" hint. True only when:
 *  - the agent is connected (can self-upgrade),
 *  - the agent reports a real version (not "dev" / empty),
 *  - we have a real latest release (not the "latest" fallback), and
 *  - the normalized versions differ.
 * Conservative by design: any ambiguity → no hint (no false positives).
 */
export function isUpdateAvailable(
  agentVersion: string | undefined,
  latestVersion: string | undefined,
  status: string,
): boolean {
  if (status !== 'connected') return false;
  if (!agentVersion || agentVersion === 'dev') return false;
  if (!latestVersion || latestVersion === 'latest') return false;
  return normalizeVersion(agentVersion) !== normalizeVersion(latestVersion);
}
