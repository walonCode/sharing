import { NativeModule, requireNativeModule } from "expo";

export type SharingStatus = {
  paired: boolean;
  laptopName: string | null;
  /** Display name of the chosen receive folder. */
  folder: string | null;
  running: boolean;
  connected: boolean;
  error: string | null;
};

type Events = {
  onStatus(status: SharingStatus): void;
};

declare class SharingServiceModule extends NativeModule<Events> {
  getStatus(): SharingStatus;
  pair(inviteUrl: string, deviceName: string): Promise<SharingStatus>;
  unpair(): Promise<SharingStatus>;
  /** Laptop folder API (JSON strings; parse with src/lib/laptop.ts). Empty path means the laptop's home. */
  listLaptopFolders(path: string): Promise<string>;
  createLaptopFolder(parent: string, name: string): Promise<string>;
  setLaptopReceiveFolder(path: string): Promise<string>;
  getLaptopSettings(): Promise<string>;
  /** Opens the system folder picker; resolves with the tree URI or null if cancelled. */
  pickReceiveFolder(): Promise<string | null>;
  /** Picks a folder and uploads it to the laptop. */
  pickFolderToSend(): Promise<string | null>;
  start(): void;
  stop(): void;
  isBatteryOptimizationIgnored(): boolean;
  requestIgnoreBatteryOptimization(): void;
}

export default requireNativeModule<SharingServiceModule>("SharingService");
