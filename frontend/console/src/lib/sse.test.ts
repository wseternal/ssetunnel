import { describe, test, expect } from 'bun:test';
import { parseSSEFrames } from './sse';

/**
 * Simulate the caller read loop from App.tsx:
 *   buffer += chunk;
 *   const frames = parseSSEFrames(buffer);
 *   const last = buffer.lastIndexOf('\n\n');
 *   buffer = last >= 0 ? buffer.slice(last + 2) : buffer;
 *
 * Returns all frames produced across the given chunks.
 */
function simulateLoop(chunks: string[]): string[] {
  let buffer = '';
  const allFrames: string[] = [];
  for (const chunk of chunks) {
    buffer += chunk;
    const frames = parseSSEFrames(buffer);
    const last = buffer.lastIndexOf('\n\n');
    buffer = last >= 0 ? buffer.slice(last + 2) : buffer;
    allFrames.push(...frames);
  }
  return allFrames;
}

describe('parseSSEFrames', () => {
  // Case 1: one complete frame in one chunk yields one frame.
  // RED: slice(5) retains the space from "data: ", producing " aGVsbG8=".
  test('one complete frame in one chunk', () => {
    const frames = parseSSEFrames('data: aGVsbG8=\n\n');
    expect(frames).toEqual([' aGVsbG8=']);
  });

  // Case 2: a frame split mid-payload yields exactly one frame after
  // both chunks through the caller loop.
  // RED: the buggy flush emits a truncated prefix, then the full frame.
  test('split frame yields exactly one frame via caller loop', () => {
    const full = 'data: aGVsbG8gd29ybGQ=\n\n';
    const splitAt = 10; // mid-payload: "data: aGVs" | "bG8gd29ybGQ=\n\n"
    const chunk1 = full.slice(0, splitAt);
    const chunk2 = full.slice(splitAt);
    const frames = simulateLoop([chunk1, chunk2]);
    expect(frames).toEqual([' aGVsbG8gd29ybGQ=']);
  });

  // Case 3: a 341 KiB-scale payload (reattach replay size), split at a
  // sweep of offsets. Assert exactly one frame and exact payload.
  test('large payload split at many offsets yields one frame', () => {
    // 256 KiB raw → ~341 KiB base64
    const rawSize = 256 * 1024;
    const raw = new Uint8Array(rawSize);
    for (let i = 0; i < rawSize; i++) raw[i] = i & 0xff;
    const b64 = btoa(String.fromCharCode(...raw));
    const frame = `data: ${b64}\n\n`;

    // Sweep split offsets: every 4097 bytes, plus mod-4 boundary offsets.
    const offsets = new Set<number>();
    for (let off = 1; off < frame.length; off += 4097) offsets.add(off);
    // Include offsets where b64 length mod 4 = 1, 2, 3 relative to "data: " prefix.
    for (const delta of [7, 8, 9, 10]) offsets.add(delta);

    for (const off of offsets) {
      if (off >= frame.length) continue;
      const chunk1 = frame.slice(0, off);
      const chunk2 = frame.slice(off);
      const frames = simulateLoop([chunk1, chunk2]);
      // RED: the buggy flush may emit a truncated prefix before the full frame.
      expect(frames.length, `offset ${off}: expected 1 frame, got ${frames.length}`).toBe(1);
      // Expect the exact base64 payload (with leading space from slice(5)).
      expect(frames[0], `offset ${off}: payload mismatch`).toBe(' ' + b64);
    }
  });

  // Case 4: atob round-trip on reassembled payload.
  test('atob round-trip on reassembled payload', () => {
    const original = 'Hello, SSE tunnel world! UTF-8: \u00e9\u00e0\u00fc';
    const b64 = btoa(original);
    const frames = simulateLoop([`data: ${b64}\n\n`]);
    expect(frames.length).toBe(1);
    expect(atob(frames[0])).toBe(original);
  });

  // Case 5: heartbeat block yields zero frames.
  test('heartbeat comment yields zero frames', () => {
    const frames = parseSSEFrames(': ka\n\n');
    expect(frames).toEqual([]);
  });

  // Case 6: multiple frames in one chunk yield all frames in order.
  // RED: slice(5) retains the space from "data: ".
  test('multiple frames in one chunk', () => {
    const frames = parseSSEFrames('data: AAAA\n\ndata: BBBB\n\ndata: CCCC\n\n');
    expect(frames).toEqual([' AAAA', ' BBBB', ' CCCC']);
  });

  // Case 7: unterminated tail is NOT emitted.
  // RED: the buggy flush emits the unterminated tail as a complete frame.
  test('unterminated tail is not emitted', () => {
    // Complete frame followed by incomplete tail.
    const text = 'data: Q1BNU0FM\n\ndata: VEhJUw==';
    const frames = parseSSEFrames(text);
    // Only the terminated frame should appear.
    expect(frames).toEqual([' Q1BNU0FM']);
  });

  // Case 7b: unterminated tail that ends after the first \n of the delimiter.
  test('tail ending after single newline is not emitted', () => {
    const text = 'data: Q1BNU0FM\n\ndata: VEhJUw==\n';
    const frames = parseSSEFrames(text);
    expect(frames).toEqual([' Q1BNU0FM']);
  });

  // Case 8: both "data: " (with space) and "data:" (no space) decode.
  test('data with and without space both decode', () => {
    // slice(5) strips "data:" only. The space variant retains a leading space.
    // This documents the current behavior. Both variants produce output.
    const framesNoSpace = parseSSEFrames('data:AAAA\n\n');
    expect(framesNoSpace).toEqual(['AAAA']);

    const framesWithSpace = parseSSEFrames('data: AAAA\n\n');
    expect(framesWithSpace).toEqual([' AAAA']);
  });
});
