/**
 * Turns whatever a native call rejected with into text a person can act on.
 * Expo wraps native failures as
 *   "Call to function 'SharingService.pair' has been rejected.\n→ Caused by: java.net.ConnectException: Failed to connect to /192.168.1.5:53317"
 * so the useful part is the cause, not the wrapper.
 */
export function friendlyError(e: unknown): string {
  const raw = e instanceof Error ? e.message : String(e);
  const cause = raw.split(/Caused by:/i).pop()!.trim();
  // Drop a leading Java class name ("java.net.ConnectException: ...").
  const detail = cause.replace(/^(?:[\w$]+\.)+[\w$]*(?:Exception|Error)(?::\s*)?/, "").trim();
  const text = `${cause} ${detail}`.toLowerCase();

  if (/unknownhost|connectexception|failed to connect|timeout|timed out|unreachable|econnrefused|no route/.test(text)) {
    return "Can't reach your laptop. Check that the phone and laptop are on the same WiFi, that sharingd is running, and that port 53317 isn't blocked by a firewall.";
  }
  if (/certificate|sslhandshake|ssl|handshake|trust anchor/.test(text)) {
    return "The laptop's security certificate didn't match. Run `sharingd pair` for a fresh QR code and try again.";
  }
  if (/expired|already used|403/.test(text)) {
    return "This pairing code has expired or was already used. Run `sharingd pair` on the laptop for a new one.";
  }
  if (/not a sharing pairing link|missing/.test(text)) {
    return "That isn't a sharing pairing link. Scan the QR from `sharingd setup`, or paste the whole sharing://pair?… link.";
  }
  return detail || cause || "Something went wrong.";
}
