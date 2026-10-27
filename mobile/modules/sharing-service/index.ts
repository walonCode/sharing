import { NativeModule, requireNativeModule } from "expo";

export type ClipboardAccess = {
  /** The user turned automatic phone-to-laptop clipboard on. */
  enabled: boolean;
  /** "Display over other apps" is allowed. */
  overlay: boolean;
  /** READ_LOGS was granted over adb. */
  logs: boolean;
  /** This build declares the permissions at all. False in the default build (`make apk-auto-clipboard` adds them). */
  supported: boolean;
  /** Android is letting the service read the log right now (null = still checking). When false, open the app to be asked. */
  logActive: boolean | null;
};

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
  /** Prompts to add the "Send clipboard" quick-settings tile; resolves "unsupported" before Android 13. */
  requestAddClipboardTile(): Promise<string>;
  clipboardAccess(): ClipboardAccess;
  /** Restarts the service so the change applies. */
  setAutoClipboard(enabled: boolean): void;
  requestOverlayPermission(): void;
  isBatteryOptimizationIgnored(): boolean;
  requestIgnoreBatteryOptimization(): void;
}

export default requireNativeModule<SharingServiceModule>("SharingService");
