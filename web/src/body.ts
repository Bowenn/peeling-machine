import type { HeaderPair } from './types';

// Go marshals []byte as base64. Decode to a UTF-8 string when possible;
// fall back to a hex dump preview for binary payloads.
export function decodeBody(b64: string | undefined): {
  text: string;
  isBinary: boolean;
  byteLength: number;
} {
  if (!b64) return { text: '', isBinary: false, byteLength: 0 };
  let bytes: Uint8Array;
  try {
    const bin = atob(b64);
    bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  } catch {
    return { text: b64, isBinary: false, byteLength: b64.length };
  }

  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
    return { text, isBinary: false, byteLength: bytes.length };
  } catch {
    return { text: hexDump(bytes), isBinary: true, byteLength: bytes.length };
  }
}

function hexDump(bytes: Uint8Array): string {
  const rows: string[] = [];
  const max = Math.min(bytes.length, 2048);
  for (let i = 0; i < max; i += 16) {
    const chunk = bytes.slice(i, i + 16);
    const hex = [...chunk].map((b) => b.toString(16).padStart(2, '0')).join(' ');
    const ascii = [...chunk].map((b) => (b >= 32 && b < 127 ? String.fromCharCode(b) : '.')).join('');
    rows.push(`${i.toString(16).padStart(8, '0')}  ${hex.padEnd(48, ' ')}  ${ascii}`);
  }
  if (bytes.length > max) rows.push(`… (${bytes.length - max} more bytes)`);
  return rows.join('\n');
}

export function findHeader(headers: HeaderPair[] | undefined, name: string): string | undefined {
  if (!headers) return undefined;
  const n = name.toLowerCase();
  for (const [k, v] of headers) if (k.toLowerCase() === n) return v;
  return undefined;
}

export function prettyPrintIfJSON(text: string, contentType?: string): string {
  const ct = contentType?.toLowerCase() ?? '';
  if (!ct.includes('json')) return text;
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
