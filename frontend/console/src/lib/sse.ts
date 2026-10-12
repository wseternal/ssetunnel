/**
 * Parse SSE frames from a text chunk and return the data payloads.
 *
 * Wire contract: each frame is `data: <base64>\n\n`.
 *
 * Split on the `\n\n` frame delimiter and process only complete frames.
 * The caller retains the unterminated tail in its buffer via
 * lastIndexOf('\n\n'). Incomplete trailing data is silently dropped.
 */
export function parseSSEFrames(text: string): string[] {
  const parts = text.split('\n\n');
  parts.pop(); // discard the unterminated tail
  const frames: string[] = [];
  for (const part of parts) {
    let data = '';
    for (const line of part.split('\n')) {
      if (line.startsWith('data:')) {
        // Strip "data:" prefix plus the optional single space.
        data += line.slice(line[5] === ' ' ? 6 : 5);
      }
    }
    if (data !== '') frames.push(data);
  }
  return frames;
}
