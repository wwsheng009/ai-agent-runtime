import { describe, expect, it } from "vitest";

import {
  parseSessionOrderAccounts,
  readSessionOrderAccounts,
  SESSION_ORDER_STORAGE_KEY,
  withSessionOrderAccount,
  writeSessionOrderAccounts,
} from "./session-order-store";

function createStorage(initial?: Record<string, string>) {
  const entries = new Map<string, string>(Object.entries(initial ?? {}));
  return {
    getItem: (key: string) => entries.get(key) ?? null,
    setItem: (key: string, value: string) => {
      entries.set(key, value);
    },
    snapshot: () => Object.fromEntries(entries),
  } as unknown as Storage & { snapshot: () => Record<string, string> };
}

function storedDocument(accounts: Record<string, unknown>, version = 1) {
  return JSON.stringify({ version, accounts });
}

describe("session-order-store", () => {
  describe("parseSessionOrderAccounts", () => {
    it("空输入不产生账目", () => {
      expect(parseSessionOrderAccounts(null)).toEqual({});
      expect(parseSessionOrderAccounts("")).toEqual({});
    });

    it("非法 JSON / 非对象一律按无账目处理", () => {
      expect(parseSessionOrderAccounts("{")).toEqual({});
      expect(parseSessionOrderAccounts('"text"')).toEqual({});
      expect(parseSessionOrderAccounts("null")).toEqual({});
      expect(parseSessionOrderAccounts("[1,2]")).toEqual({});
    });

    it("未知版本不猜测，按无账目处理", () => {
      expect(
        parseSessionOrderAccounts(storedDocument({ dir: ["a"] }, 99)),
      ).toEqual({});
    });

    it("accounts 非对象时按无账目处理", () => {
      expect(
        parseSessionOrderAccounts(JSON.stringify({ version: 1, accounts: 3 })),
      ).toEqual({});
    });

    it("保留合法账目：忽略非字符串、重复项与空串，并裁掉两端空白", () => {
      expect(
        parseSessionOrderAccounts(
          storedDocument({
            dir: [" a ", "a", 7, "", "b"],
            other: ["x"],
          }),
        ),
      ).toEqual({ dir: ["a", "b"], other: ["x"] });
    });

    it("空账目与空键不落库", () => {
      expect(
        parseSessionOrderAccounts(storedDocument({ dir: [], "  ": ["a"] })),
      ).toEqual({});
    });
  });

  describe("readSessionOrderAccounts", () => {
    it("无存储时返回空账目（不抛）", () => {
      expect(readSessionOrderAccounts(null)).toEqual({});
      expect(readSessionOrderAccounts(undefined)).toEqual({});
    });

    it("读取并按同一口径解析", () => {
      const storage = createStorage({
        [SESSION_ORDER_STORAGE_KEY]: storedDocument({ dir: ["b", "a"] }),
      });
      expect(readSessionOrderAccounts(storage)).toEqual({ dir: ["b", "a"] });
    });

    it("存储读取抛错时退化为空账目", () => {
      const storage = {
        getItem: () => {
          throw new Error("blocked");
        },
      } as unknown as Storage;
      expect(readSessionOrderAccounts(storage)).toEqual({});
    });
  });

  describe("writeSessionOrderAccounts", () => {
    it("写入带版本号的文档，跳过空账目", () => {
      const storage = createStorage();
      writeSessionOrderAccounts(storage, { dir: ["a", "b"], empty: [] });

      expect(
        JSON.parse(storage.snapshot()[SESSION_ORDER_STORAGE_KEY] ?? "{}"),
      ).toEqual({ version: 1, accounts: { dir: ["a", "b"] } });
    });

    it("写入失败不抛出（配额 / 隐私模式）", () => {
      const storage = {
        setItem: () => {
          throw new Error("quota");
        },
      } as unknown as Storage;

      expect(() => writeSessionOrderAccounts(storage, { dir: ["a"] })).not.toThrow();
    });

    it("写入内容可被自身解析回来（往返）", () => {
      const storage = createStorage();
      writeSessionOrderAccounts(storage, { dir: ["c", "a"] });

      expect(readSessionOrderAccounts(storage)).toEqual({ dir: ["c", "a"] });
    });
  });

  describe("withSessionOrderAccount", () => {
    it("顺序未变化时返回原引用", () => {
      const accounts = { dir: ["a", "b"] };
      expect(withSessionOrderAccount(accounts, "dir", ["a", "b"])).toBe(accounts);
    });

    it("新键写入新对象，不改动原对象", () => {
      const accounts = { dir: ["a"] };
      const next = withSessionOrderAccount(accounts, "other", ["b"]);

      expect(next).toEqual({ dir: ["a"], other: ["b"] });
      expect(accounts).toEqual({ dir: ["a"] });
      expect(next).not.toBe(accounts);
    });

    it("空顺序或空键不落库", () => {
      const accounts = { dir: ["a"] };
      expect(withSessionOrderAccount(accounts, "dir", [])).toBe(accounts);
      expect(withSessionOrderAccount(accounts, "  ", ["b"])).toBe(accounts);
    });

    it("写入的是副本：外部改动不影响账目", () => {
      const order = ["a", "b"];
      const next = withSessionOrderAccount({}, "dir", order);
      order.push("c");

      expect(next.dir).toEqual(["a", "b"]);
    });
  });
});
