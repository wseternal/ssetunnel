/**
 * A parsed SSE event with its type and data payload.
 *
 * Wire contract: each event is terminated by `\n\n`. Fields within an
 * event are separated by `\n`. Recognized fields:
 *   - `event: <type>`  — event type (defaults to '' for data-only frames)
 *   - `data: <value>`  — event data (concatenated across multiple data: lines)
 *
 * The optional single space after the colon is stripped for both fields.
 */
export interface SSEEvent {
  type: string;
  data: string;
}

/**
 * Parse complete SSE events from a streaming text buffer.
 *
 * Splits on the `\n\n` frame delimiter. Returns only fully terminated
 * events; the unterminated trailing data is returned as `remaining` so
 * the caller can prepend it to the next chunk.
 *
 * An event is emitted when it carries either a typed payload (`event:`)
 * or a data payload (`data:`). Untyped data-only frames with empty
 * data are filtered — those are server heartbeats ("data:\n\n"), and
 * the frontend should treat absence of any event as the watchdog
 * signal instead.
 */
export function parseSSEEvents(text: string): { events: SSEEvent[]; remaining: string } {
  const parts = text.split('\n\n');
  const remaining = parts.pop() || '';
  const events: SSEEvent[] = [];
  for (const part of parts) {
    let type = '';
    let data = '';
    for (const line of part.split('\n')) {
      if (line.startsWith('data:')) {
        data += line.slice(line[5] === ' ' ? 6 : 5);
      } else if (line.startsWith('event:')) {
        type = line.slice(line[6] === ' ' ? 7 : 6);
      }
    }
    // Emit any event that has either a type or non-empty data.
    // Heartbeats (untyped + empty data) are filtered.
    if (data !== '' || type !== '') events.push({ type, data });
  }
  return { events, remaining };
}
