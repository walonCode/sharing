import { describe, expect, test } from "bun:test";
import { parseInvite } from "./invite";

const fp = "ab".repeat(32);
const link = `sharing://pair?fp=${fp}&host=192.168.1.5&name=my+laptop&port=53317&token=abc_DEF-123`;

describe("parseInvite", () => {
  test("parses a link produced by sharingd", () => {
    expect(parseInvite(link)).toEqual({
      host: "192.168.1.5",
      port: 53317,
      fingerprint: fp,
      token: "abc_DEF-123",
      name: "my laptop",
    });
  });

  test("tolerates surrounding whitespace and uppercase fingerprints", () => {
    const parsed = parseInvite(`  ${link.replace(fp, fp.toUpperCase())}\n`);
    expect(parsed?.fingerprint).toBe(fp);
  });

  test("defaults the name", () => {
    expect(parseInvite(link.replace("&name=my+laptop", ""))?.name).toBe("laptop");
  });

  test.each([
    ["wrong scheme", link.replace("sharing://", "https://")],
    ["missing token", link.replace("&token=abc_DEF-123", "")],
    ["missing host", link.replace("&host=192.168.1.5", "")],
    ["short fingerprint", link.replace(fp, "abcd")],
    ["non-hex fingerprint", link.replace(fp, "zz".repeat(32))],
    ["port zero", link.replace("53317", "0")],
    ["port too large", link.replace("53317", "70000")],
    ["port not a number", link.replace("53317", "abc")],
    ["empty", ""],
  ])("rejects %s", (_name, value) => {
    expect(parseInvite(value)).toBeNull();
  });
});
