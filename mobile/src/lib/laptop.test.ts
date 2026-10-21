import { expect, mock, test } from "bun:test";

// SharingService needs the native runtime, so stub it before importing the wrapper.
mock.module("./sharing", () => ({
  SharingService: {
    listLaptopFolders: async (p: string) => JSON.stringify({ path: p || "/home/me", parent: "", dirs: ["a"] }),
    createLaptopFolder: async (parent: string, name: string) => JSON.stringify({ path: `${parent}/${name}` }),
    setLaptopReceiveFolder: async (p: string) => JSON.stringify({ receiveDir: p }),
    getLaptopSettings: async () => JSON.stringify({ receiveDir: "/home/me/Downloads/Sharing" }),
  },
}));

const { listFolders, createFolder, setReceiveFolder, getReceiveFolder, displayPath } = await import("./laptop");

test("wrappers parse the laptop's JSON", async () => {
  expect(await listFolders()).toEqual({ path: "/home/me", parent: "", dirs: ["a"] });
  expect(await createFolder("/home/me", "Phone")).toBe("/home/me/Phone");
  expect(await setReceiveFolder("/home/me/Phone")).toBe("/home/me/Phone");
  expect(await getReceiveFolder()).toBe("/home/me/Downloads/Sharing");
});

test("displayPath abbreviates the home folder only on a path boundary", () => {
  expect(displayPath("/home/me", "/home/me")).toBe("~");
  expect(displayPath("/home/me/Docs", "/home/me")).toBe("~/Docs");
  expect(displayPath("/home/melvin/Docs", "/home/me")).toBe("/home/melvin/Docs");
});
