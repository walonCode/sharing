import { describe, expect, test } from "bun:test";
import { friendlyError } from "./errors";

const wrap = (cause: string) =>
  new Error(`Call to function 'SharingService.pair' has been rejected.\n→ Caused by: ${cause}`);

describe("friendlyError", () => {
  test("unwraps the expo wrapper and explains network failures", () => {
    const msg = friendlyError(wrap("java.net.ConnectException: Failed to connect to /192.168.43.166:53317"));
    expect(msg).toContain("same WiFi");
    expect(msg).not.toContain("Call to function");
  });

  test("timeouts are network problems too", () => {
    expect(friendlyError(wrap("java.net.SocketTimeoutException: timeout"))).toContain("same WiFi");
  });

  test("certificate mismatch", () => {
    expect(friendlyError(wrap("javax.net.ssl.SSLHandshakeException: Certificate does not match the paired laptop"))).toContain("sharingd pair");
  });

  test("expired invite", () => {
    expect(friendlyError(wrap("java.lang.IllegalStateException: Pairing link expired or already used. Run `sharingd pair` again."))).toContain("expired");
  });

  test("bad link", () => {
    expect(friendlyError(wrap("java.lang.IllegalArgumentException: Not a sharing pairing link"))).toContain("isn't a sharing pairing link");
  });

  test("unknown errors keep their message minus the class name", () => {
    expect(friendlyError(wrap("java.lang.IllegalStateException: No receive folder chosen"))).toBe("No receive folder chosen");
  });

  test("handles non-Error values", () => {
    expect(friendlyError("boom")).toBe("boom");
    expect(friendlyError(undefined)).toBe("undefined");
  });
});
