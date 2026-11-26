// A minimal ZIP writer (stored, no compression) so exporting a project needs
// no dependency. Enough for a handful of small text files.

const CRC = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

function crc32(data: Uint8Array): number {
  let c = 0xffffffff;
  for (const b of data) c = CRC[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

/** Packs `files` (path -> text) into a .zip, optionally under a top-level folder. */
export function zip(files: Record<string, string>, folder = ""): Blob {
  const enc = new TextEncoder();
  const now = new Date();
  const time = (now.getHours() << 11) | (now.getMinutes() << 5) | (now.getSeconds() >> 1);
  const date = ((now.getFullYear() - 1980) << 9) | ((now.getMonth() + 1) << 5) | now.getDate();
  const parts: Uint8Array[] = [];
  const central: Uint8Array[] = [];
  let offset = 0;

  for (const path of Object.keys(files).sort()) {
    const name = enc.encode(folder ? `${folder}/${path}` : path);
    const data = enc.encode(files[path]);
    const crc = crc32(data);
    // Shared by the local header (after its signature) and the central entry.
    const common = new DataView(new ArrayBuffer(26));
    common.setUint16(0, 20, true); // version needed
    common.setUint16(2, 0x0800, true); // UTF-8 names
    common.setUint16(4, 0, true); // stored
    common.setUint16(6, time, true);
    common.setUint16(8, date, true);
    common.setUint32(10, crc, true);
    common.setUint32(14, data.length, true);
    common.setUint32(18, data.length, true);
    common.setUint16(22, name.length, true);
    common.setUint16(24, 0, true); // extra length

    const local = new Uint8Array(30 + name.length);
    new DataView(local.buffer).setUint32(0, 0x04034b50, true);
    local.set(new Uint8Array(common.buffer), 4);
    local.set(name, 30);

    const entry = new Uint8Array(46 + name.length);
    const ev = new DataView(entry.buffer);
    ev.setUint32(0, 0x02014b50, true);
    ev.setUint16(4, 20, true); // version made by
    entry.set(new Uint8Array(common.buffer), 6);
    ev.setUint32(42, offset, true); // comment, disk, attributes stay 0
    entry.set(name, 46);

    parts.push(local, data);
    central.push(entry);
    offset += local.length + data.length;
  }

  const size = central.reduce((n, e) => n + e.length, 0);
  const end = new DataView(new ArrayBuffer(22));
  end.setUint32(0, 0x06054b50, true);
  end.setUint16(8, central.length, true);
  end.setUint16(10, central.length, true);
  end.setUint32(12, size, true);
  end.setUint32(16, offset, true);
  return new Blob([...parts, ...central, new Uint8Array(end.buffer)] as BlobPart[], { type: "application/zip" });
}

/** Offers `blob` to the user as a file download. */
export function download(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
