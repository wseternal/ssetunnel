import { describe, test, expect } from 'bun:test';
import { parseSSEEvents } from './sse';

/**
 * Simulate the caller read loop using the parser's remaining buffer.
 * Returns all event data payloads produced across the given chunks.
 */
function simulateLoop(chunks: string[]): string[] {
  let buffer = '';
  const allData: string[] = [];
  for (const chunk of chunks) {
    buffer += chunk;
    const { events, remaining } = parseSSEEvents(buffer);
    buffer = remaining;
    allData.push(...events.map(e => e.data));
  }
  return allData;
}

describe('parseSSEEvents', () => {
  // Case 1: one complete frame in one chunk yields one event.
  test('one complete frame in one chunk', () => {
    const { events, remaining } = parseSSEEvents('data: aGVsbG8=\n\n');
    expect(events.length).toBe(1);
    expect(events[0].data).toBe('aGVsbG8=');
    expect(events[0].type).toBe('');
    expect(remaining).toBe('');
  });

  // Case 2: a frame split mid-payload yields exactly one event after
  // both chunks through the caller loop.
  test('split frame yields exactly one event via caller loop', () => {
    const full = 'data: aGVsbG8gd29ybGQ=\n\n';
    const splitAt = 10; // mid-payload: "data: aGVs" | "bG8gd29ybGQ=\n\n"
    const chunk1 = full.slice(0, splitAt);
    const chunk2 = full.slice(splitAt);
    const data = simulateLoop([chunk1, chunk2]);
    expect(data).toEqual(['aGVsbG8gd29ybGQ=']);
  });

  // Case 3: a 341 KiB-scale payload (reattach replay size), split at a
  // sweep of offsets. Assert exactly one event and exact payload.
  test('large payload split at many offsets yields one event', () => {
    const rawSize = 256 * 1024;
    const raw = new Uint8Array(rawSize);
    for (let i = 0; i < rawSize; i++) raw[i] = i & 0xff;
    const b64 = btoa(String.fromCharCode(...raw));
    const frame = `data: ${b64}\n\n`;

    const offsets = new Set<number>();
    for (let off = 1; off < frame.length; off += 4097) offsets.add(off);
    for (const delta of [7, 8, 9, 10]) offsets.add(delta);

    for (const off of offsets) {
      if (off >= frame.length) continue;
      const chunk1 = frame.slice(0, off);
      const chunk2 = frame.slice(off);
      const data = simulateLoop([chunk1, chunk2]);
      expect(data.length, `offset ${off}: expected 1 event, got ${data.length}`).toBe(1);
      expect(data[0], `offset ${off}: payload mismatch`).toBe(b64);
    }
  });

  // Case 4: atob round-trip on reassembled payload.
  test('atob round-trip on reassembled payload', () => {
    const original = 'Hello, SSE tunnel world! UTF-8: \u00e9\u00e0\u00fc';
    const b64 = btoa(original);
    const data = simulateLoop([`data: ${b64}\n\n`]);
    expect(data.length).toBe(1);
    expect(atob(data[0])).toBe(original);
  });

  // Case 5: heartbeat block yields zero events.
  test('heartbeat comment yields zero events', () => {
    const { events, remaining } = parseSSEEvents(': ka\n\n');
    expect(events).toEqual([]);
    expect(remaining).toBe('');
  });

  // Case 6: multiple frames in one chunk yield all events in order.
  test('multiple frames in one chunk', () => {
    const { events } = parseSSEEvents('data: AAAA\n\ndata: BBBB\n\ndata: CCCC\n\n');
    expect(events.map(e => e.data)).toEqual(['AAAA', 'BBBB', 'CCCC']);
  });

  // Case 7: unterminated tail is NOT emitted.
  test('unterminated tail is not emitted', () => {
    const { events, remaining } = parseSSEEvents('data: Q1BNU0FM\n\ndata: VEhJUw==');
    expect(events.map(e => e.data)).toEqual(['Q1BNU0FM']);
    expect(remaining).toBe('data: VEhJUw==');
  });

  // Case 7b: unterminated tail that ends after the first \n of the delimiter.
  test('tail ending after single newline is not emitted', () => {
    const { events, remaining } = parseSSEEvents('data: Q1BNU0FM\n\ndata: VEhJUw==\n');
    expect(events.map(e => e.data)).toEqual(['Q1BNU0FM']);
    expect(remaining).toBe('data: VEhJUw==\n');
  });

  // Case 8: both "data: " (with space) and "data:" (no space) decode.
  test('data with and without space both decode', () => {
    const r1 = parseSSEEvents('data:AAAA\n\n');
    expect(r1.events[0].data).toBe('AAAA');

    const r2 = parseSSEEvents('data: AAAA\n\n');
    expect(r2.events[0].data).toBe('AAAA');
  });

  // Edge: empty input yields no events.
  test('empty input yields no events', () => {
    const { events, remaining } = parseSSEEvents('');
    expect(events).toEqual([]);
    expect(remaining).toBe('');
  });

  // Edge: standalone unterminated frame yields no events.
  test('standalone unterminated frame yields no events', () => {
    const { events, remaining } = parseSSEEvents('data: AAAA');
    expect(events).toEqual([]);
    expect(remaining).toBe('data: AAAA');
  });

  // Edge: heartbeat followed by data in same chunk yields only data.
  test('heartbeat followed by data in same chunk', () => {
    const { events } = parseSSEEvents(': ka\n\ndata: AAAA\n\n');
    expect(events.map(e => e.data)).toEqual(['AAAA']);
  });

  // Typed event: event: field is parsed.
  test('typed event parses event type', () => {
    const { events } = parseSSEEvents('event: screeninfo\ndata: eyJ3aWR0aCI6MTkyMH0=\n\n');
    expect(events.length).toBe(1);
    expect(events[0].type).toBe('screeninfo');
    expect(events[0].data).toBe('eyJ3aWR0aCI6MTkyMH0=');
  });

  // Typed event without space after "event:"
  test('event field without space', () => {
    const { events } = parseSSEEvents('event:tune\ndata: AAAA\n\n');
    expect(events[0].type).toBe('tune');
    expect(events[0].data).toBe('AAAA');
  });

  // Mixed typed and untyped events in one chunk.
  test('mixed typed and untyped events', () => {
    const text = 'event: fps\ndata: RlBT\n\ndata: RlJBTUU=\n\nevent: log\ndata: TE9H\n\n';
    const { events } = parseSSEEvents(text);
    expect(events.length).toBe(3);
    expect(events[0].type).toBe('fps');
    expect(events[0].data).toBe('RlBT');
    expect(events[1].type).toBe('');
    expect(events[1].data).toBe('RlJBTUU=');
    expect(events[2].type).toBe('log');
    expect(events[2].data).toBe('TE9H');
  });

  // Remaining buffer is correctly carried across chunks.
  test('remaining buffer carries partial data to next chunk', () => {
    const chunk1 = 'data: AAAA\n\ndata: BB';
    const r1 = parseSSEEvents(chunk1);
    expect(r1.events.map(e => e.data)).toEqual(['AAAA']);
    expect(r1.remaining).toBe('data: BB');

    const chunk2 = r1.remaining + 'BB\n\n';
    const r2 = parseSSEEvents(chunk2);
    expect(r2.events.map(e => e.data)).toEqual(['BBBB']);
    expect(r2.remaining).toBe('');
  });

  // Complete frame ending with \n\n leaves empty remaining.
  test('complete frame leaves empty remaining', () => {
    const { remaining } = parseSSEEvents('data: AAAA\n\n');
    expect(remaining).toBe('');
  });
});
