"""Рисует иконку Zero Launcher и собирает rsrc_windows_amd64.syso для go build.

Запуск из корня проекта:  python tools/make_icon.py   (нужен Pillow)
.syso — объектный файл COFF с ресурсами Windows: иконка, манифест и сведения о версии
(их показывают «Свойства» файла и Диспетчер задач). Компоновщик Go подхватывает его сам.
"""

import io
import os
import struct

from PIL import Image, ImageDraw

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SIZES = [256, 64, 48, 32, 24, 16]

NIGHT = (18, 15, 44, 255)
GRAD = ((255, 216, 74), (255, 154, 31), (255, 106, 0))  # как в логотипе интерфейса

# Логотип «Z» в координатах 48×48 — тот же, что в web/index.html и favicon.svg
Z_SHAPE = [(11, 6), (44, 6), (40.8, 13.4), (22.6, 33.5), (39, 33.5), (35.8, 41), (3, 41), (6.2, 33.6), (24.4, 13.5), (7.8, 13.5)]
Z_BAR = [(13.2, 21.6), (35.8, 21.6), (34.2, 25.6), (11.6, 25.6)]


def gradient(s):
    g = Image.new("RGBA", (s, s))
    px = g.load()
    for y in range(s):
        for x in range(s):
            t = (x + y) / (2 * (s - 1))
            a, b = (GRAD[0], GRAD[1]) if t < 0.55 else (GRAD[1], GRAD[2])
            u = t / 0.55 if t < 0.55 else (t - 0.55) / 0.45
            px[x, y] = tuple(int(a[i] + (b[i] - a[i]) * u) for i in range(3)) + (255,)
    return g


def draw(size):
    s = size * 4  # рисуем крупно и уменьшаем — так края гладкие
    im = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.rounded_rectangle([0, 0, s - 1, s - 1], radius=int(s * 0.22), fill=NIGHT)
    k = s / 48
    pt = lambda p: ((p[0] * 0.84 + 4) * k, (p[1] * 0.84 + 4) * k)  # noqa: E731
    mask = Image.new("L", (s, s), 0)
    md = ImageDraw.Draw(mask)
    md.polygon([pt(p) for p in Z_SHAPE], fill=255)
    md.polygon([pt(p) for p in Z_BAR], fill=255)
    im.paste(gradient(s), (0, 0), mask)
    return im.resize((size, size), Image.LANCZOS)


def png_bytes(im):
    buf = io.BytesIO()
    im.save(buf, "PNG")
    return buf.getvalue()


def dib_bytes(im):
    """32-битный DIB для иконок меньше 256 px: так их понимают все версии Windows."""
    im = im.convert("RGBA")
    w, h = im.size
    header = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, 0, 0, 0, 0, 0)
    px = im.load()
    xor = bytearray()
    for y in range(h - 1, -1, -1):  # снизу вверх
        for x in range(w):
            r, g, b, a = px[x, y]
            xor += bytes((b, g, r, a))
    row = ((w + 31) // 32) * 4
    and_mask = bytearray(row * h)  # прозрачность берётся из альфа-канала
    return header + bytes(xor) + bytes(and_mask)


VERSION = (2, 0, 2, 0)
STRINGS = {
    "CompanyName": "Zero Launcher",
    "FileDescription": "Zero Launcher",
    "FileVersion": "2.0.2",
    "InternalName": "ZeroLauncher",
    "LegalCopyright": "Zero Launcher",
    "OriginalFilename": "ZeroLauncher.exe",
    "ProductName": "Zero Launcher",
    "ProductVersion": "2.0.2",
}

MANIFEST = b"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="ZeroLauncher" version="2.0.2.0" processorArchitecture="amd64"/>
  <description>Zero Launcher</description>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0"
        processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
      <supportedOS Id="{1f676c76-80e1-4239-95bb-83d0f6d0da78}"/>
    </application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
    </windowsSettings>
  </application>
</assembly>
"""


def _pad4(b):
    return b + b"\0" * ((4 - len(b) % 4) % 4)


def _node(key, value=b"", value_len=0, typ=1, children=()):
    """Узел VS_VERSIONINFO: wLength, wValueLength, wType, szKey, выравнивание, Value, дети."""
    body = _pad4(b"\0" * 6 + (key + "\0").encode("utf-16-le"))
    if value:
        body = _pad4(body + value)
    for c in children:
        body = _pad4(body) + c
    return struct.pack("<HHH", len(body), value_len, typ) + body[6:]


def version_info():
    ms = (VERSION[0] << 16) | VERSION[1]
    ls = (VERSION[2] << 16) | VERSION[3]
    fixed = struct.pack("<13I", 0xFEEF04BD, 0x00010000, ms, ls, ms, ls, 0x3F, 0, 0x00040004, 1, 0, 0, 0)
    strings = []
    for k, v in STRINGS.items():
        val = (v + "\0").encode("utf-16-le")
        strings.append(_node(k, val, len(v) + 1, 1))
    table = _node("040904b0", children=strings)
    sfi = _node("StringFileInfo", children=[table])
    var = _node("Translation", struct.pack("<HH", 0x0409, 0x04B0), 4, 0)
    vfi = _node("VarFileInfo", children=[var])
    return _node("VS_VERSION_INFO", fixed, len(fixed), 0, children=[sfi, vfi])


def build_syso(images, path):
    """images: [(size, bytes)] → COFF .syso с .rsrc для amd64."""
    RT_ICON, RT_GROUP_ICON, RT_VERSION, RT_MANIFEST, LANG = 3, 14, 16, 24, 0x0409

    # Дерево ресурсов: тип → имя/ID → язык → IMAGE_RESOURCE_DATA_ENTRY
    leaves = []  # (type, id, data)
    for i, (_, data) in enumerate(images, start=1):
        leaves.append((RT_ICON, i, data))
    grp = struct.pack("<HHH", 0, 1, len(images))
    for i, (size, data) in enumerate(images, start=1):
        dim = 0 if size >= 256 else size
        grp += struct.pack("<BBBBHHIH", dim, dim, 0, 0, 1, 32, len(data), i)
    leaves.append((RT_GROUP_ICON, 1, grp))
    leaves.append((RT_VERSION, 1, version_info()))
    leaves.append((RT_MANIFEST, 1, MANIFEST))

    types = {}
    for t, i, data in leaves:
        types.setdefault(t, []).append((i, data))

    def dir_header(n_ids):
        return struct.pack("<IIHHHH", 0, 0, 0, 0, 0, n_ids)

    # Раскладка: корень, каталоги имён, каталоги языков, записи данных, сами данные.
    sorted_types = sorted(types)
    root_size = 16 + 8 * len(sorted_types)
    offset = root_size
    name_dirs = {}
    for t in sorted_types:
        name_dirs[t] = offset
        offset += 16 + 8 * len(types[t])
    lang_dirs = {}
    for t in sorted_types:
        for i, _ in sorted(types[t]):
            lang_dirs[(t, i)] = offset
            offset += 16 + 8
    entries = {}
    for t in sorted_types:
        for i, _ in sorted(types[t]):
            entries[(t, i)] = offset
            offset += 16
    data_off = {}
    for t in sorted_types:
        for i, data in sorted(types[t]):
            offset = (offset + 7) & ~7
            data_off[(t, i)] = offset
            offset += len(data)
    total = (offset + 7) & ~7

    sec = bytearray(total)
    relocs = []

    def put(pos, raw):
        sec[pos:pos + len(raw)] = raw

    put(0, dir_header(len(sorted_types)))
    for n, t in enumerate(sorted_types):
        put(16 + 8 * n, struct.pack("<II", t, 0x80000000 | name_dirs[t]))
    for t in sorted_types:
        base = name_dirs[t]
        items = sorted(types[t])
        put(base, dir_header(len(items)))
        for n, (i, _) in enumerate(items):
            put(base + 16 + 8 * n, struct.pack("<II", i, 0x80000000 | lang_dirs[(t, i)]))
            lb = lang_dirs[(t, i)]
            put(lb, dir_header(1))
            put(lb + 16, struct.pack("<II", LANG, entries[(t, i)]))
    for t in sorted_types:
        for i, data in sorted(types[t]):
            e = entries[(t, i)]
            # OffsetToData — RVA; пишем смещение внутри секции, компоновщик добавит RVA секции.
            put(e, struct.pack("<IIII", data_off[(t, i)], len(data), 0, 0))
            relocs.append(e)
            put(data_off[(t, i)], data)

    n_rel = len(relocs)
    file_hdr = 20
    sec_hdr = 40
    raw_ptr = file_hdr + sec_hdr
    rel_ptr = raw_ptr + total
    sym_ptr = rel_ptr + 10 * n_rel

    out = bytearray()
    out += struct.pack("<HHIIIHH", 0x8664, 1, 0, sym_ptr, 1, 0, 0)
    out += struct.pack("<8sIIIIIIHHI", b".rsrc", 0, 0, total, raw_ptr, rel_ptr, 0, n_rel, 0, 0x40000040)
    out += sec
    for pos in relocs:
        out += struct.pack("<IIH", pos, 0, 0x0003)  # IMAGE_REL_AMD64_ADDR32NB к символу секции
    out += struct.pack("<8sIhHBB", b".rsrc", 0, 1, 0, 3, 0)  # символ секции, IMAGE_SYM_CLASS_STATIC
    out += struct.pack("<I", 4)  # пустая таблица строк
    with open(path, "wb") as f:
        f.write(out)


def main():
    images = [(s, png_bytes(draw(s)) if s >= 256 else dib_bytes(draw(s))) for s in SIZES]
    draw(256).save(os.path.join(ROOT, "zero.ico"), sizes=[(s, s) for s in sorted(SIZES)])
    draw(256).save(os.path.join(ROOT, "tools", "icon-256.png"))
    build_syso(images, os.path.join(ROOT, "rsrc_windows_amd64.syso"))
    print("ok: zero.ico, rsrc_windows_amd64.syso")


if __name__ == "__main__":
    main()
