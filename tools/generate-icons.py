#!/usr/bin/env python3
"""Rasterize the geometric b. mark in web/icon.svg using only the standard library."""
import pathlib
import struct
import zlib

ROOT = pathlib.Path(__file__).resolve().parents[1] / 'web'
BACKGROUND = (45, 75, 64)
FOREGROUND = (247, 245, 239)


def marked(x, y):
    stem = 162 <= x <= 200 and 132 <= y <= 300
    bowl = (x - 234) ** 2 + (y - 300) ** 2 <= 72 ** 2
    hole = (x - 234) ** 2 + (y - 300) ** 2 < 34 ** 2
    dot = (x - 330) ** 2 + (y - 350) ** 2 <= 16 ** 2
    return ((stem or bowl) and not hole) or dot


def chunk(kind, data):
    return struct.pack('!I', len(data)) + kind + data + struct.pack('!I', zlib.crc32(kind + data))


for size, name in [(192, 'pwa-192.png'), (512, 'pwa-512.png'), (180, 'apple-touch-icon.png')]:
    rows = bytearray()
    samples = 4
    for y in range(size):
        rows.append(0)
        for x in range(size):
            coverage = sum(marked((x + (sx + .5) / samples) * 512 / size,
                                  (y + (sy + .5) / samples) * 512 / size)
                           for sy in range(samples) for sx in range(samples)) / samples ** 2
            rows.extend(round(bg + (fg - bg) * coverage) for bg, fg in zip(BACKGROUND, FOREGROUND))
    png = b'\x89PNG\r\n\x1a\n'
    png += chunk(b'IHDR', struct.pack('!2I5B', size, size, 8, 2, 0, 0, 0))
    png += chunk(b'IDAT', zlib.compress(rows, 9)) + chunk(b'IEND', b'')
    (ROOT / name).write_bytes(png)
