// Wails bridge adapter. Uses generated Wails bindings when the Wails
// runtime is detected; otherwise falls back to the HTTP bridge.
// This file is a placeholder — the actual Wails bindings will be
// generated into app/clix/frontend/bindings/ by `wails3 generate bindings`.
// The bridge.ts selector chooses this adapter when window.runtime exists.

import type { Bridge } from './types';

// WailsBridge will be populated once bindings are generated.
// For now, it serves as the integration point for Task 15.
export const wailsBridge: Bridge | null = null;
