package main

// Иконки из .exe и .ico на чистом Go: без PowerShell и без System.Drawing,
// поэтому берётся самая крупная картинка (обычно 256×256), а не 32×32.

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	maxIconFile = 8 << 20  // .ico больше 8 МБ не бывает
	maxRsrcSize = 96 << 20 // секция ресурсов, которую готовы прочитать
)

var errNoIcon = errors.New("иконка не найдена")

var pngSig = []byte("\x89PNG\r\n\x1a\n")

// iconEntry — одна картинка внутри .ico или группы иконок .exe.
type iconEntry struct {
	w, h int
	bpp  int
	data []byte
}

func (e iconEntry) better(o iconEntry) bool {
	if a, b := e.w*e.h, o.w*o.h; a != b {
		return a > b
	}
	return e.bpp > o.bpp
}

// exeIconPNG достаёт главную иконку программы (первую группу RT_GROUP_ICON) в PNG.
func exeIconPNG(path string) ([]byte, error) { return exeIconIndexPNG(path, 0) }

// exeIconIndexPNG — иконка с номером, как в ярлыке Windows «файл,номер»:
// номер ≥ 0 — по порядку групп, меньше нуля — по идентификатору ресурса.
func exeIconIndexPNG(path string, index int) ([]byte, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var dd pe.DataDirectory
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		if oh.NumberOfRvaAndSizes > 2 {
			dd = oh.DataDirectory[2]
		}
	case *pe.OptionalHeader64:
		if oh.NumberOfRvaAndSizes > 2 {
			dd = oh.DataDirectory[2]
		}
	}
	if dd.VirtualAddress == 0 || dd.Size == 0 {
		return nil, errNoIcon
	}
	img := &peImage{f: f, cache: map[*pe.Section][]byte{}}
	dir, ok := img.section(dd.VirtualAddress)
	if !ok {
		return nil, errNoIcon
	}
	return iconFromResources(dir, img.read, index)
}

// peImage читает данные по виртуальному адресу из любой секции: у упакованных
// программ каталог ресурсов и сами картинки иногда лежат в разных секциях.
type peImage struct {
	f     *pe.File
	cache map[*pe.Section][]byte
}

func (p *peImage) find(rva uint32) (*pe.Section, []byte, bool) {
	for _, s := range p.f.Sections {
		size := s.VirtualSize
		if s.Size > size {
			size = s.Size
		}
		if rva < s.VirtualAddress || rva >= s.VirtualAddress+size {
			continue
		}
		if s.Size > maxRsrcSize {
			return nil, nil, false
		}
		data, ok := p.cache[s]
		if !ok {
			var err error
			if data, err = s.Data(); err != nil {
				return nil, nil, false
			}
			p.cache[s] = data
		}
		return s, data, true
	}
	return nil, nil, false
}

// section — данные от адреса rva до конца его секции (каталог ресурсов).
func (p *peImage) section(rva uint32) ([]byte, bool) {
	s, data, ok := p.find(rva)
	if !ok || uint64(rva-s.VirtualAddress) > uint64(len(data)) {
		return nil, false
	}
	return data[rva-s.VirtualAddress:], true
}

func (p *peImage) read(rva, size uint32) ([]byte, bool) {
	s, data, ok := p.find(rva)
	if !ok {
		return nil, false
	}
	start := uint64(rva - s.VirtualAddress)
	if start+uint64(size) > uint64(len(data)) {
		return nil, false
	}
	return data[start : start+uint64(size)], true
}

type rsrcEntry struct {
	id     uint32
	named  bool
	offset uint32
	dir    bool
}

// rsrcDir читает каталог ресурсов; смещения считаются от начала каталога ресурсов (dir).
func rsrcDir(dir []byte, off uint32) ([]rsrcEntry, bool) {
	if uint64(off)+16 > uint64(len(dir)) {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint16(dir[off+12:])) + int(binary.LittleEndian.Uint16(dir[off+14:]))
	if n > 4096 || uint64(off)+16+uint64(n)*8 > uint64(len(dir)) {
		return nil, false
	}
	out := make([]rsrcEntry, 0, n)
	for i := 0; i < n; i++ {
		e := off + 16 + uint32(i)*8
		name := binary.LittleEndian.Uint32(dir[e:])
		to := binary.LittleEndian.Uint32(dir[e+4:])
		out = append(out, rsrcEntry{id: name & 0x7fffffff, named: name&0x80000000 != 0, offset: to & 0x7fffffff, dir: to&0x80000000 != 0})
	}
	return out, true
}

// rsrcLeaf спускается по каталогам (имя → язык) до первого блока данных.
func rsrcLeaf(dir []byte, read func(rva, size uint32) ([]byte, bool), e rsrcEntry) ([]byte, bool) {
	for depth := 0; e.dir; depth++ {
		if depth > 4 {
			return nil, false
		}
		sub, ok := rsrcDir(dir, e.offset)
		if !ok || len(sub) == 0 {
			return nil, false
		}
		e = sub[0]
	}
	if uint64(e.offset)+16 > uint64(len(dir)) {
		return nil, false
	}
	rva := binary.LittleEndian.Uint32(dir[e.offset:])
	size := binary.LittleEndian.Uint32(dir[e.offset+4:])
	if size == 0 || size > maxIconFile {
		return nil, false
	}
	return read(rva, size)
}

// iconFromResources: dir — каталог ресурсов, read — чтение данных по адресу, index — какая группа иконок нужна.
func iconFromResources(dir []byte, read func(rva, size uint32) ([]byte, bool), index int) ([]byte, error) {
	types, ok := rsrcDir(dir, 0)
	if !ok {
		return nil, errNoIcon
	}
	var groups, icons []rsrcEntry
	for _, t := range types {
		if t.named || !t.dir {
			continue
		}
		list, ok := rsrcDir(dir, t.offset)
		if !ok {
			continue
		}
		switch t.id {
		case rtGroupIcon:
			groups = list
		case rtIcon:
			icons = list
		}
	}
	if len(groups) == 0 || len(icons) == 0 {
		return nil, errNoIcon
	}
	byID := map[uint32]rsrcEntry{}
	for _, e := range icons {
		if !e.named {
			byID[e.id] = e
		}
	}
	// Проводник показывает первую группу: сначала именованные, затем по номеру — как в каталоге.
	order := groups
	switch {
	case index > 0 && index < len(groups):
		order = append([]rsrcEntry{groups[index]}, groups...)
	case index < 0:
		for _, g := range groups {
			if !g.named && int(g.id) == -index {
				order = append([]rsrcEntry{g}, groups...)
				break
			}
		}
	}
	for _, g := range order {
		grp, ok := rsrcLeaf(dir, read, g)
		if !ok || len(grp) < 6 || binary.LittleEndian.Uint16(grp[2:]) != 1 {
			continue
		}
		count := int(binary.LittleEndian.Uint16(grp[4:]))
		var best *iconEntry
		for i := 0; i < count; i++ {
			o := 6 + i*14
			if o+14 > len(grp) {
				break
			}
			e, ok := byID[uint32(binary.LittleEndian.Uint16(grp[o+12:]))]
			if !ok {
				continue
			}
			raw, ok := rsrcLeaf(dir, read, e)
			if !ok {
				continue
			}
			ent := iconEntry{w: dimOrDefault(grp[o]), h: dimOrDefault(grp[o+1]), bpp: int(binary.LittleEndian.Uint16(grp[o+6:])), data: raw}
			if best == nil || ent.better(*best) {
				c := ent
				best = &c
			}
		}
		if best != nil {
			if out, err := entryPNG(*best); err == nil {
				return out, nil
			}
		}
	}
	return nil, errNoIcon
}

func dimOrDefault(b byte) int {
	if b == 0 {
		return 256
	}
	return int(b)
}

// icoFilePNG берёт самую крупную картинку из файла .ico.
func icoFilePNG(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxIconFile {
		return nil, errors.New("файл иконки слишком большой")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return icoPNG(raw)
}

func icoPNG(raw []byte) ([]byte, error) {
	if len(raw) < 6 || binary.LittleEndian.Uint16(raw[2:]) != 1 {
		return nil, errNoIcon
	}
	count := int(binary.LittleEndian.Uint16(raw[4:]))
	var best *iconEntry
	for i := 0; i < count; i++ {
		o := 6 + i*16
		if o+16 > len(raw) {
			break
		}
		size := uint64(binary.LittleEndian.Uint32(raw[o+8:]))
		off := uint64(binary.LittleEndian.Uint32(raw[o+12:]))
		if off+size > uint64(len(raw)) || size == 0 {
			continue
		}
		ent := iconEntry{w: dimOrDefault(raw[o]), h: dimOrDefault(raw[o+1]), bpp: int(binary.LittleEndian.Uint16(raw[o+6:])), data: raw[off : off+size]}
		if best == nil || ent.better(*best) {
			c := ent
			best = &c
		}
	}
	if best == nil {
		return nil, errNoIcon
	}
	return entryPNG(*best)
}

// entryPNG: PNG внутри иконки отдаётся как есть, старый формат (DIB) перекодируется в PNG.
func entryPNG(e iconEntry) ([]byte, error) {
	if bytes.HasPrefix(e.data, pngSig) {
		if _, err := png.DecodeConfig(bytes.NewReader(e.data)); err != nil {
			return nil, err
		}
		return e.data, nil
	}
	img, err := decodeDIB(e.data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeDIB разбирает картинку иконки в формате BITMAPINFOHEADER: цвета + маска прозрачности.
func decodeDIB(b []byte) (image.Image, error) {
	bad := errors.New("повреждённая иконка")
	if len(b) < 40 {
		return nil, bad
	}
	hdr := int(binary.LittleEndian.Uint32(b))
	w := int(int32(binary.LittleEndian.Uint32(b[4:])))
	h2 := int(int32(binary.LittleEndian.Uint32(b[8:])))
	bpp := int(binary.LittleEndian.Uint16(b[14:]))
	comp := binary.LittleEndian.Uint32(b[16:])
	used := int(binary.LittleEndian.Uint32(b[32:]))
	if hdr < 40 || hdr > len(b) || w <= 0 || w > 1024 || h2 == 0 {
		return nil, bad
	}
	topDown := h2 < 0
	if topDown {
		h2 = -h2
	}
	h := h2 / 2
	if h <= 0 || h > 1024 {
		return nil, bad
	}
	if comp != 0 && !(comp == 3 && bpp == 32) {
		return nil, errors.New("сжатая иконка не поддерживается")
	}
	off := hdr
	if comp == 3 && hdr == 40 {
		off += 12 // маски каналов BI_BITFIELDS
	}
	var pal []color.NRGBA
	if bpp <= 8 {
		n := used
		if n == 0 || n > 1<<bpp {
			n = 1 << bpp
		}
		if off+n*4 > len(b) {
			return nil, bad
		}
		for i := 0; i < n; i++ {
			p := b[off+i*4:]
			pal = append(pal, color.NRGBA{p[2], p[1], p[0], 255})
		}
		off += n * 4
	}
	switch bpp {
	case 1, 4, 8, 24, 32:
	default:
		return nil, errors.New("неизвестный формат иконки")
	}
	xorStride := ((w*bpp + 31) / 32) * 4
	andStride := ((w + 31) / 32) * 4
	if off+xorStride*h > len(b) {
		return nil, bad
	}
	andOff := off + xorStride*h
	hasMask := andOff+andStride*h <= len(b)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	alphaSeen := false
	for y := 0; y < h; y++ {
		row := y
		if !topDown {
			row = h - 1 - y
		}
		line := b[off+row*xorStride:]
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch bpp {
			case 32:
				p := line[x*4:]
				c = color.NRGBA{p[2], p[1], p[0], p[3]}
				if p[3] != 0 {
					alphaSeen = true
				}
			case 24:
				p := line[x*3:]
				c = color.NRGBA{p[2], p[1], p[0], 255}
			default:
				idx := 0
				switch bpp {
				case 8:
					idx = int(line[x])
				case 4:
					idx = int(line[x/2]>>(4*(1-x%2))) & 0x0f
				case 1:
					idx = int(line[x/8]>>(7-x%8)) & 1
				}
				if idx < len(pal) {
					c = pal[idx]
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	if bpp == 32 && !alphaSeen && !hasMask { // альфа пустая и маски нет — картинка непрозрачная
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	// Маска AND: у 32-битных иконок с альфа-каналом не нужна, у остальных задаёт прозрачность.
	if hasMask && !(bpp == 32 && alphaSeen) {
		for y := 0; y < h; y++ {
			row := y
			if !topDown {
				row = h - 1 - y
			}
			line := b[andOff+row*andStride:]
			for x := 0; x < w; x++ {
				c := img.NRGBAAt(x, y)
				if line[x/8]>>(7-x%8)&1 == 1 {
					c.A = 0
				} else {
					c.A = 255
				}
				img.SetNRGBA(x, y, c)
			}
		}
	}
	return img, nil
}
