import type { QueuedDictation } from "./dictation-queue";

export type SavedDictation = Pick<QueuedDictation, "id" | "scope" | "target" | "data" | "language" | "created" | "capture"> & {
  reason: string;
};
export type SavedDictationInfo = Omit<SavedDictation, "data">;
const STORAGE_TIMEOUT_MS = 2000;

// Recovery is separate from the live queue. Opening the database never queues a clip.
export class DictationRecovery {
  private db?: Promise<IDBDatabase>;

  private open() {
    return this.db ??= new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("tmx-dictation-recovery", 1);
      let abandoned = false;
      const abandon = () => {
        abandoned = true;
        reject(new Error("Saved dictations are unavailable on this device."));
      };
      const timer = setTimeout(abandon, STORAGE_TIMEOUT_MS);
      request.onupgradeneeded = () => request.result.createObjectStore("clips", { keyPath: "id" });
      request.onerror = () => { clearTimeout(timer); reject(request.error); };
      request.onblocked = () => { clearTimeout(timer); abandon(); };
      request.onsuccess = () => {
        clearTimeout(timer);
        const db = request.result;
        if (abandoned) { db.close(); return; }
        db.onversionchange = () => { db.close(); this.db = undefined; };
        resolve(db);
      };
    }).catch(error => {
      this.db = undefined;
      throw error;
    });
  }

  private async run<T>(mode: IDBTransactionMode, action: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
    const db = await this.open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction("clips", mode);
      const request = action(tx.objectStore("clips"));
      const timer = setTimeout(() => {
        try { tx.abort(); } catch { /* transaction already closed */ }
        reject(new Error("Saved dictation storage timed out."));
      }, STORAGE_TIMEOUT_MS);
      tx.oncomplete = () => { clearTimeout(timer); resolve(request.result); };
      tx.onabort = tx.onerror = () => { clearTimeout(timer); reject(tx.error ?? request.error); };
    });
  }

  list() { return this.run("readonly", store => store.getAll() as IDBRequest<SavedDictation[]>); }
  get(id: string) { return this.run("readonly", store => store.get(id) as IDBRequest<SavedDictation | undefined>); }
  put(clip: SavedDictation) { return this.run("readwrite", store => store.put(clip)); }
  remove(id: string) { return this.run("readwrite", store => store.delete(id)); }
}
