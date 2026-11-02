package main

// Картинки, .ico и .exe для тестов и демо: всё рисуется здесь же, без чужих файлов.

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// По умолчанию тесты не ходят в интернет за обложками; отдельные тесты разрешают свой httptest-сервер.
var realArtURLAllowed = artURLAllowed

func TestMain(m *testing.M) {
	artURLAllowed = func(*url.URL) bool { return false }
	os.Exit(m.Run())
}

func gradientImage(w, h int, a, b color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x+y) / float64(w+h)
			img.Set(x, y, color.RGBA{
				uint8(float64(a.R)*(1-t) + float64(b.R)*t), uint8(float64(a.G)*(1-t) + float64(b.G)*t),
				uint8(float64(a.B)*(1-t) + float64(b.B)*t), 255})
		}
	}
	return img
}

func pngBytes(t testing.TB, img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t testing.TB, img image.Image) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeBytes(t testing.TB, path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dib32 — картинка иконки старого формата: 32 бита BGRA снизу вверх и маска AND.
func dib32(w, h int, c color.NRGBA) []byte {
	var b bytes.Buffer
	hdr := struct {
		Size                  uint32
		W, H                  int32
		Planes, Bits          uint16
		Comp, ImgSize, XR, YR uint32
		ClrUsed, ClrImportant uint32
	}{40, int32(w), int32(h * 2), 1, 32, 0, 0, 0, 0, 0, 0}
	_ = binary.Write(&b, binary.LittleEndian, hdr)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, ((w+31)/32)*4*h))
	return b.Bytes()
}

// dib8 — 8-битная иконка с палитрой; левая половина прозрачна по маске.
func dib8(w, h int, c color.NRGBA) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint32{40})
	_ = binary.Write(&b, binary.LittleEndian, []int32{int32(w), int32(h * 2)})
	_ = binary.Write(&b, binary.LittleEndian, []uint16{1, 8})
	_ = binary.Write(&b, binary.LittleEndian, []uint32{0, 0, 0, 0, 2, 0})
	b.Write([]byte{0, 0, 0, 0, c.B, c.G, c.R, 0})
	stride := ((w*8 + 31) / 32) * 4
	for y := 0; y < h; y++ {
		row := make([]byte, stride)
		for x := 0; x < w; x++ {
			row[x] = 1
		}
		b.Write(row)
	}
	mstride := ((w + 31) / 32) * 4
	for y := 0; y < h; y++ {
		row := make([]byte, mstride)
		for x := 0; x < w/2; x++ {
			row[x/8] |= 1 << (7 - x%8)
		}
		b.Write(row)
	}
	return b.Bytes()
}

type testIcon struct {
	w, h int
	bpp  int
	data []byte
}

func icoFile(icons []testIcon) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 1, uint16(len(icons))})
	off := 6 + 16*len(icons)
	for _, ic := range icons {
		b.Write([]byte{byte(ic.w % 256), byte(ic.h % 256), 0, 0})
		_ = binary.Write(&b, binary.LittleEndian, []uint16{1, uint16(ic.bpp)})
		_ = binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(ic.data)), uint32(off)})
		off += len(ic.data)
	}
	for _, ic := range icons {
		b.Write(ic.data)
	}
	return b.Bytes()
}

// rsrcBuild собирает каталог ресурсов: RT_ICON со всеми картинками и по группе RT_GROUP_ICON на каждый набор.
// dirVA — адрес каталога, dataVA — адрес, где будут лежать сами данные (0 — сразу за каталогом).
func rsrcBuild(dirVA, dataVA uint32, groups [][]testIcon) (dir, blobs []byte) {
	var all []testIcon
	var groupBlobs [][]byte
	for _, icons := range groups {
		group := new(bytes.Buffer)
		_ = binary.Write(group, binary.LittleEndian, []uint16{0, 1, uint16(len(icons))})
		for _, ic := range icons {
			all = append(all, ic)
			group.Write([]byte{byte(ic.w % 256), byte(ic.h % 256), 0, 0})
			_ = binary.Write(group, binary.LittleEndian, []uint16{1, uint16(ic.bpp)})
			_ = binary.Write(group, binary.LittleEndian, []uint32{uint32(len(ic.data))})
			_ = binary.Write(group, binary.LittleEndian, []uint16{uint16(len(all))})
		}
		groupBlobs = append(groupBlobs, group.Bytes())
	}
	var data [][]byte
	for _, ic := range all {
		data = append(data, ic.data)
	}
	data = append(data, groupBlobs...)

	mkdir := func(entries [][2]uint32) []byte { // [id, смещение | флаг каталога]
		var b bytes.Buffer
		_ = binary.Write(&b, binary.LittleEndian, []uint32{0, 0})
		_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 0, 0, uint16(len(entries))})
		for _, e := range entries {
			_ = binary.Write(&b, binary.LittleEndian, e)
		}
		return b.Bytes()
	}
	n, g := len(all), len(groups)
	const isDir = 0x80000000
	typeIcon := uint32(16 + 2*8)
	typeGroup := typeIcon + uint32(16+n*8)
	langStart := typeGroup + uint32(16+g*8)
	entries := langStart + uint32((n+g)*(16+8))
	dirSize := entries + uint32((n+g)*16)
	if dataVA == 0 {
		dataVA = dirVA + dirSize
	}

	var out bytes.Buffer
	out.Write(mkdir([][2]uint32{{rtIcon, typeIcon | isDir}, {rtGroupIcon, typeGroup | isDir}}))
	var iconDirs, groupDirs [][2]uint32
	for i := 0; i < n; i++ {
		iconDirs = append(iconDirs, [2]uint32{uint32(i + 1), (langStart + uint32(i*24)) | isDir})
	}
	for k := 0; k < g; k++ {
		groupDirs = append(groupDirs, [2]uint32{uint32(k + 1), (langStart + uint32((n+k)*24)) | isDir})
	}
	out.Write(mkdir(iconDirs))
	out.Write(mkdir(groupDirs))
	for i := 0; i < n+g; i++ { // каталоги языков указывают на записи данных
		out.Write(mkdir([][2]uint32{{0x409, entries + uint32(i*16)}}))
	}
	var blob bytes.Buffer
	for _, d := range data {
		_ = binary.Write(&out, binary.LittleEndian, []uint32{dataVA + uint32(blob.Len()), uint32(len(d)), 0, 0})
		blob.Write(d)
		for blob.Len()%4 != 0 {
			blob.WriteByte(0)
		}
	}
	return out.Bytes(), blob.Bytes()
}

// rsrcSection — каталог и данные подряд, как в обычной секции .rsrc.
func rsrcSection(secVA uint32, icons []testIcon) []byte {
	dir, blobs := rsrcBuild(secVA, 0, [][]testIcon{icons})
	return append(dir, blobs...)
}

func peWithIcons(t testing.TB, icons []testIcon) []byte {
	return peWithIconGroups(t, [][]testIcon{icons}, false)
}

// peWithIconGroups — минимальный PE32+ с ресурсами. split — данные иконок в отдельной секции, как у упакованных программ.
func peWithIconGroups(t testing.TB, groups [][]testIcon, split bool) []byte {
	const secVA, fileAlign = 0x1000, 0x200
	align := func(n, a int) int { return (n + a - 1) &^ (a - 1) }
	var sections [][]byte
	var names []string
	var vas []uint32
	if split {
		probe, _ := rsrcBuild(secVA, 0, groups)
		dataVA := uint32(secVA + align(len(probe), 0x1000))
		dir, blobs := rsrcBuild(secVA, dataVA, groups)
		sections, names, vas = [][]byte{dir, blobs}, []string{".rsrc", ".rdata2"}, []uint32{secVA, dataVA}
	} else {
		dir, blobs := rsrcBuild(secVA, 0, groups)
		sections, names, vas = [][]byte{append(dir, blobs...)}, []string{".rsrc"}, []uint32{secVA}
	}
	var b bytes.Buffer
	dos := make([]byte, 0x40)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], 0x40)
	b.Write(dos)
	b.WriteString("PE\x00\x00")
	_ = binary.Write(&b, binary.LittleEndian, pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, NumberOfSections: uint16(len(sections)),
		SizeOfOptionalHeader: 240, Characteristics: 0x22})
	last := len(sections) - 1
	oh := pe.OptionalHeader64{Magic: 0x20b, SectionAlignment: 0x1000, FileAlignment: fileAlign, ImageBase: 0x140000000,
		MajorOperatingSystemVersion: 6, MajorSubsystemVersion: 6, SizeOfImage: vas[last] + uint32(align(len(sections[last]), 0x1000)),
		SizeOfHeaders: fileAlign, Subsystem: 2, NumberOfRvaAndSizes: 16}
	oh.DataDirectory[2] = pe.DataDirectory{VirtualAddress: secVA, Size: uint32(len(sections[0]))}
	_ = binary.Write(&b, binary.LittleEndian, oh)
	ptr := fileAlign
	for i, sec := range sections {
		raw := align(len(sec), fileAlign)
		sh := pe.SectionHeader32{VirtualSize: uint32(len(sec)), VirtualAddress: vas[i], SizeOfRawData: uint32(raw),
			PointerToRawData: uint32(ptr), Characteristics: 0x40000040}
		copy(sh.Name[:], names[i])
		_ = binary.Write(&b, binary.LittleEndian, sh)
		ptr += raw
	}
	for b.Len() < fileAlign {
		b.WriteByte(0)
	}
	for _, sec := range sections {
		start := b.Len()
		b.Write(sec)
		for b.Len() < start+align(len(sec), fileAlign) {
			b.WriteByte(0)
		}
	}
	if b.Len() < 0x400 {
		t.Fatal("PE слишком короткий")
	}
	return b.Bytes()
}

// lnkFile — ярлык Windows: путь к программе, значок «файл,номер» и аргументы (строки в Unicode).
func lnkFile(target, iconPath string, iconIndex int32, args string) []byte {
	flags := uint32(lnkHasLinkInfo | lnkIsUnicode)
	if args != "" {
		flags |= lnkHasArgs
	}
	if iconPath != "" {
		flags |= lnkHasIconLoc
	}
	hdr := make([]byte, 0x4c)
	binary.LittleEndian.PutUint32(hdr, 0x4c)
	copy(hdr[4:], []byte{0x01, 0x14, 0x02, 0, 0, 0, 0, 0, 0xc0, 0, 0, 0, 0, 0, 0, 0x46})
	binary.LittleEndian.PutUint32(hdr[0x14:], flags)
	binary.LittleEndian.PutUint32(hdr[0x38:], uint32(iconIndex))
	var b bytes.Buffer
	b.Write(hdr)
	vol := []byte{0x11, 0, 0, 0, 3, 0, 0, 0, 1, 2, 3, 4, 0x10, 0, 0, 0, 0}
	base := append([]byte(target), 0)
	const liHdr = 0x1c
	volOff, baseOff := liHdr, liHdr+len(vol)
	sufOff := baseOff + len(base)
	li := make([]byte, liHdr)
	binary.LittleEndian.PutUint32(li[0:], uint32(sufOff+1))
	binary.LittleEndian.PutUint32(li[4:], liHdr)
	binary.LittleEndian.PutUint32(li[8:], 1)
	binary.LittleEndian.PutUint32(li[12:], uint32(volOff))
	binary.LittleEndian.PutUint32(li[16:], uint32(baseOff))
	binary.LittleEndian.PutUint32(li[24:], uint32(sufOff))
	b.Write(li)
	b.Write(vol)
	b.Write(base)
	b.WriteByte(0)
	str := func(s string) {
		u := utf16.Encode([]rune(s))
		_ = binary.Write(&b, binary.LittleEndian, uint16(len(u)))
		_ = binary.Write(&b, binary.LittleEndian, u)
	}
	if args != "" {
		str(args)
	}
	if iconPath != "" {
		str(iconPath)
	}
	b.Write([]byte{0, 0, 0, 0})
	return b.Bytes()
}

func iconPNG(t testing.TB, size int, c color.RGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy < (size/2-1)*(size/2-1) {
				img.Set(x, y, c)
			}
		}
	}
	return pngBytes(t, img)
}
