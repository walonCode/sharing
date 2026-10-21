export type Invite = {
  host: string;
  port: number;
  fingerprint: string;
  token: string;
  name: string;
};

/**
 * Validates a `sharing://pair?...` link (from the laptop's QR) before it is
 * handed to the native module. Returns null for anything that is not one.
 */
export function parseInvite(raw: string): Invite | null {
  const match = /^sharing:\/\/pair\?(.*)$/.exec(raw.trim());
  if (!match) return null;
  const q = new URLSearchParams(match[1]);
  const host = q.get("host");
  const token = q.get("token");
  const fingerprint = q.get("fp")?.toLowerCase();
  const port = Number(q.get("port"));
  if (!host || !token || !fingerprint) return null;
  if (!/^[0-9a-f]{64}$/.test(fingerprint)) return null;
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
  return { host, port, fingerprint, token, name: q.get("name") || "laptop" };
}
