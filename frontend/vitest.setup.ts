// Zustand's persist middleware is initialized when store modules are imported. Node has no
// localStorage, so provide a small standards-shaped in-memory implementation before test imports.
// This keeps store tests honest about serialization and avoids noisy "storage unavailable"
// warnings without pulling a browser DOM into the otherwise pure Node test suite.
const values = new Map<string, string>();

const localStorage: Storage = {
  get length() {
    return values.size;
  },
  clear() {
    values.clear();
  },
  getItem(key: string) {
    return values.get(key) ?? null;
  },
  key(index: number) {
    return [...values.keys()][index] ?? null;
  },
  removeItem(key: string) {
    values.delete(key);
  },
  setItem(key: string, value: string) {
    values.set(key, value);
  },
};

Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: localStorage,
});

// Zustand's default storage factory reads window.localStorage specifically.
Object.defineProperty(globalThis, 'window', {
  configurable: true,
  value: { localStorage },
  writable: true,
});
