import { SharingService } from "./sharing";

export type FolderListing = { path: string; parent: string; dirs: string[] };

/** Typed wrappers over the native laptop-folder calls (which return raw JSON). */
export async function listFolders(path = ""): Promise<FolderListing> {
  return JSON.parse(await SharingService.listLaptopFolders(path));
}

export async function createFolder(parent: string, name: string): Promise<string> {
  return JSON.parse(await SharingService.createLaptopFolder(parent, name)).path;
}

export async function setReceiveFolder(path: string): Promise<string> {
  return JSON.parse(await SharingService.setLaptopReceiveFolder(path)).receiveDir;
}

export async function getReceiveFolder(): Promise<string> {
  return JSON.parse(await SharingService.getLaptopSettings()).receiveDir;
}

/** "/home/me/Documents/Phone" -> "~/Documents/Phone" for display. */
export function displayPath(path: string, home: string): string {
  return path === home ? "~" : path.startsWith(home + "/") ? "~" + path.slice(home.length) : path;
}
