import { NativeModule, requireNativeModule } from "expo";

export type SharingStatus = {
  paired: boolean;
  laptopName: string | null;
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
  /** Picks a folder and uploads it to the laptop. */
  pickFolderToSend(): Promise<string | null>;
  start(): void;
  stop(): void;
  isBatteryOptimizationIgnored(): boolean;
  requestIgnoreBatteryOptimization(): void;
}

export default requireNativeModule<SharingServiceModule>("SharingService");
