// Backup files through the browser (docs/BACKUP.md §8 step 5): the upload
// for "A backup file on my computer". XMLHttpRequest rather than fetch,
// for upload progress on a file of up to about 2 GB.
import { CSRF_HEADER, csrfToken } from "@/api/client";

/** The largest backup file the server takes (MaxUploadSize, about 2 GB). */
export const MAX_BACKUP_FILE = 2 * 1024 ** 3 + 128 * 1024 ** 2;

/** The stored upload, or the server's problem+json body (for problemMessage/needsConfirm). */
export type UploadResult = { upload_id: string; size: number } | { problem: unknown };

export function uploadBackupFile(file: Blob, onProgress: (fraction: number) => void): Promise<UploadResult> {
  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", "/api/v1/backup-restore/file");
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    const token = csrfToken();
    if (token) xhr.setRequestHeader(CSRF_HEADER, token);
    xhr.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded / e.total); };
    xhr.onload = () => {
      let body: unknown = null;
      try { body = JSON.parse(xhr.responseText); } catch { /* not JSON */ }
      if (xhr.status === 201 && body && typeof body === "object" && "upload_id" in body) {
        resolve(body as { upload_id: string; size: number });
      } else {
        resolve({ problem: body ?? { detail: "The file didn't reach the server. Try again." } });
      }
    };
    xhr.onerror = () => resolve({ problem: { detail: "The file didn't reach the server. Check the connection and try again." } });
    xhr.send(file);
  });
}

/** A size in plain words: "340 MB", "1.2 GB". */
export function formatSize(bytes: number): string {
  if (bytes < 1024 ** 2) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  if (bytes < 1024 ** 3) return `${Math.round(bytes / 1024 ** 2)} MB`;
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}
