import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, isUnavailable } from "./api";

afterEach(() => vi.unstubAllGlobals());

function respond(status: number, body: string, ok = status < 400) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok,
      status,
      statusText: `status ${status}`,
      text: async () => body,
    })),
  );
}

describe("the API client's error handling", () => {
  it("surfaces the node's own message", async () => {
    respond(409, JSON.stringify({ error: "bot changed; read it again and retry" }));
    await expect(api.capabilities()).rejects.toThrow("bot changed; read it again and retry");
  });

  it("carries the status, so a conflict is distinguishable from a typo", async () => {
    respond(409, JSON.stringify({ error: "conflict" }));
    await expect(api.capabilities()).rejects.toMatchObject({ status: 409 });
    respond(404, JSON.stringify({ error: "no such bot" }));
    await expect(api.capabilities()).rejects.toBeInstanceOf(ApiError);
  });

  it("keeps a non-JSON body instead of a parser error", async () => {
    respond(502, "<html><body>Bad Gateway</body></html>");
    await expect(api.capabilities()).rejects.toThrow(/Bad Gateway/);
  });

  it("falls back to the status text when there is no body at all", async () => {
    respond(500, "");
    await expect(api.capabilities()).rejects.toThrow("status 500");
  });
});

describe("discovery", () => {
  it("treats a missing groups route as no teams, not a failure", async () => {
    respond(404, JSON.stringify({ error: "not found" }));
    await expect(api.listGroups()).resolves.toEqual([]);
  });

  it("does not treat an unavailable backend as an empty team list", async () => {
    respond(503, JSON.stringify({ error: "agent not configured on this node" }));
    await expect(api.listGroups()).rejects.toMatchObject({ status: 503 });
  });

  it("flags network and 503 errors as unavailable", () => {
    expect(isUnavailable(new TypeError("Failed to fetch"))).toBe(true);
    expect(isUnavailable(new ApiError(503, "down"))).toBe(true);
    expect(isUnavailable(new ApiError(404, "missing"))).toBe(false);
  });
});
