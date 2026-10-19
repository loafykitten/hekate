"""Remove Blender's wall-clock PNG metadata without altering encoded pixels."""
import struct
import zlib


def normalize(raw):
    if raw[:8] != b'\x89PNG\r\n\x1a\n':
        raise ValueError('Invalid PNG signature')
    result = bytearray(raw[:8])
    offset = 8
    ended = False
    while offset < len(raw):
        if ended or offset + 12 > len(raw):
            raise ValueError('Invalid PNG chunk boundary')
        size = struct.unpack_from('>I', raw, offset)[0]
        end = offset + size + 12
        if end > len(raw):
            raise ValueError('Truncated PNG chunk')
        kind = raw[offset + 4:offset + 8]
        data = raw[offset + 8:end - 4]
        crc = struct.unpack_from('>I', raw, end - 4)[0]
        if zlib.crc32(kind + data) != crc:
            raise ValueError('Invalid PNG checksum')
        volatile = kind == b'tEXt' and data.split(b'\0', 1)[0] in (b'Date', b'RenderTime')
        if not volatile:
            result.extend(raw[offset:end])
        ended = kind == b'IEND'
        offset = end
    if not ended:
        raise ValueError('Missing PNG end chunk')
    return bytes(result)


def normalize_folder(folder):
    for path in sorted(folder.glob('*.png')):
        original = path.read_bytes()
        normalized = normalize(original)
        if normalized != original:
            path.write_bytes(normalized)
