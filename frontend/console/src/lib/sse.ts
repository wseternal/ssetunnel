/**
 * Parse SSE frames from a text chunk and return the data payloads.
 *
 * Wire contract: each frame is `data: <base64>\n\n`.
 * This function also accepts `data:` (no space) for robustness.
 *
 * NOTE: The current implementation flushes an unterminated trailing frame.
 * The caller already retains the unterminated tail via lastIndexOf('\n\n').
 * The flush causes a split frame to be emitted twice — once truncated,
 * once complete — which corrupts xterm output for large frames.
 */
export function parseSSEFrames(text: string): string[] {
  const frames: string[] = [];
  const lines = text.split('\n');
  let currentData = '';
  for (const line of lines) {
    if (line.startsWith('data:')) {
      currentData += line.slice(5);
    } else if (line === '' && currentData !== '') {
      frames.push(currentData);
      currentData = '';
    }
  }
  if (currentData !== '') frames.push(currentData);
  return frames;
}
