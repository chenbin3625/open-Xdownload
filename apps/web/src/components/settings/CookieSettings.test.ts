import { describe, expect, it } from "vitest";
import {
  isRedactedBackupCookieRow,
  normalizeBackupCookieRows,
  parseBackupCookieRows,
  parseBackupCookieRowsFromJSON,
  splitCookieKeyValue,
} from "./CookieSettings";

describe("parseBackupCookieRows", () => {
  it("parses cookie pairs from mixed delimiters", () => {
    const rows = parseBackupCookieRows("auth_token=aaa; ct0=bbb\nauth_token=ccc; ct0=ddd");
    expect(rows.map((row) => ({ authToken: row.authToken, csrfToken: row.csrfToken }))).toEqual([
      { authToken: "aaa", csrfToken: "bbb" },
      { authToken: "ccc", csrfToken: "ddd" },
    ]);
  });

  it("keeps redacted secrets as a single row", () => {
    const rows = parseBackupCookieRows("********");
    expect(rows).toHaveLength(1);
    expect(isRedactedBackupCookieRow(rows[0])).toBe(true);
  });
});
describe("parseBackupCookieRowsFromJSON", () => {
  it("reads id/authToken/ct0 aliases", () => {
    const rows = parseBackupCookieRowsFromJSON(
      JSON.stringify([{ id: "row-1", auth_token: "a", ct0: "b" }]),
    );
    expect(rows).toEqual([{ id: "row-1", authToken: "a", csrfToken: "b" }]);
  });
});

describe("normalizeBackupCookieRows", () => {
  it("drops empty rows and round-trips JSON", () => {
    expect(normalizeBackupCookieRows([
      { id: "1", authToken: " a ", csrfToken: " b " },
      { id: "2", authToken: "", csrfToken: "" },
    ])).toBe(JSON.stringify([{ id: "1", authToken: "a", csrfToken: "b" }]));
  });
});

describe("splitCookieKeyValue", () => {
  it("splits on = or : and strips quotes", () => {
    expect(splitCookieKeyValue('auth_token="abc"')).toEqual(["auth_token", "abc"]);
    expect(splitCookieKeyValue("ct0:xyz")).toEqual(["ct0", "xyz"]);
  });
});
