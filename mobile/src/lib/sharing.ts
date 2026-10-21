import { useEffect, useState } from "react";
import SharingService, { type SharingStatus } from "../../modules/sharing-service";

export { SharingService };
export type { SharingStatus };

/** Live status of the background service, pushed from native on every change. */
export function useSharingStatus(): SharingStatus {
  const [status, setStatus] = useState<SharingStatus>(() => SharingService.getStatus());
  useEffect(() => {
    const sub = SharingService.addListener("onStatus", setStatus);
    return () => sub.remove();
  }, []);
  return status;
}
