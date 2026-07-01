// SRT Extractor — subtitle extraction and management for Windows.
// Copyright (C) 2026 fromdisposition
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version.
//
// This program is distributed in the hope that it will be useful, but WITHOUT
// ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
// FOR A PARTICULAR PURPOSE. See the GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License along with
// this program. If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtManifest  = 24
)

type resource struct {
	typ  uint32
	id   uint32
	lang uint32
	data []byte
}

func main() {
	manifestPath, iconPath, out := "srtextractor.exe.manifest", "icon.png", "rsrc_windows_amd64.syso"
	if len(os.Args) > 3 {
		manifestPath, iconPath, out = os.Args[1], os.Args[2], os.Args[3]
	}

	var res []resource
	if b, err := os.ReadFile(manifestPath); err == nil {
		res = append(res, resource{rtManifest, 1, 0x409, b})
	} else {
		fmt.Fprintln(os.Stderr, "manifest:", err)
		os.Exit(1)
	}
	if png, err := os.ReadFile(iconPath); err == nil {
		grp := buildGroupIcon(png, 1)
		res = append(res, resource{rtIcon, 1, 0x409, png})
		res = append(res, resource{rtGroupIcon, 1, 0x409, grp})
	} else {
		fmt.Fprintln(os.Stderr, "icon (skipped):", err)
	}

	if err := os.WriteFile(out, buildSyso(res), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s with %d resources\n", out, len(res))
}

func buildGroupIcon(png []byte, iconID uint16) []byte {
	w, h := byte(0), byte(0)
	if len(png) >= 24 {
		pw := binary.BigEndian.Uint32(png[16:20])
		ph := binary.BigEndian.Uint32(png[20:24])
		if pw < 256 {
			w = byte(pw)
		}
		if ph < 256 {
			h = byte(ph)
		}
	}
	var b bytes.Buffer
	bw := func(v interface{}) { binary.Write(&b, binary.LittleEndian, v) }

	bw(uint16(0))
	bw(uint16(1))
	bw(uint16(1))

	b.WriteByte(w)
	b.WriteByte(h)
	b.WriteByte(0)
	b.WriteByte(0)
	bw(uint16(1))
	bw(uint16(32))
	bw(uint32(len(png)))
	bw(iconID)
	return b.Bytes()
}

func buildSyso(res []resource) []byte {

	types := map[uint32][]resource{}
	for _, r := range res {
		types[r.typ] = append(types[r.typ], r)
	}
	var typeKeys []uint32
	for k := range types {
		typeKeys = append(typeKeys, k)
		sort.Slice(types[k], func(i, j int) bool { return types[k][i].id < types[k][j].id })
	}
	sort.Slice(typeKeys, func(i, j int) bool { return typeKeys[i] < typeKeys[j] })

	const dirHdr = 16
	const entry = 8
	const dataEntry = 16

	nIds := 0
	for _, k := range typeKeys {
		nIds += len(types[k])
	}
	off := uint32(0)
	rootOff := off
	off += dirHdr + uint32(len(typeKeys))*entry
	typeDirOff := map[uint32]uint32{}
	for _, k := range typeKeys {
		typeDirOff[k] = off
		off += dirHdr + uint32(len(types[k]))*entry
	}

	type idKey struct{ typ, id uint32 }
	nameDirOff := map[idKey]uint32{}
	for _, k := range typeKeys {
		for _, r := range types[k] {
			nameDirOff[idKey{k, r.id}] = off
			off += dirHdr + entry
		}
	}

	dataEntOff := map[idKey]uint32{}
	for _, k := range typeKeys {
		for _, r := range types[k] {
			dataEntOff[idKey{k, r.id}] = off
			off += dataEntry
		}
	}

	blobOff := map[idKey]uint32{}
	for _, k := range typeKeys {
		for _, r := range types[k] {
			blobOff[idKey{k, r.id}] = off
			off += uint32(len(r.data))
			for off%4 != 0 {
				off++
			}
		}
	}
	sectionSize := off

	buf := make([]byte, sectionSize)
	put32 := func(at, v uint32) { binary.LittleEndian.PutUint32(buf[at:], v) }
	put16 := func(at uint32, v uint16) { binary.LittleEndian.PutUint16(buf[at:], v) }

	writeDirHeader := func(at uint32, nIdEntries uint16) {

		put16(at+12, 0)
		put16(at+14, nIdEntries)
	}

	writeDirHeader(rootOff, uint16(len(typeKeys)))
	ePos := rootOff + dirHdr
	for _, k := range typeKeys {
		put32(ePos, k)
		put32(ePos+4, typeDirOff[k]|0x80000000)
		ePos += entry
	}

	for _, k := range typeKeys {
		writeDirHeader(typeDirOff[k], uint16(len(types[k])))
		ep := typeDirOff[k] + dirHdr
		for _, r := range types[k] {
			put32(ep, r.id)
			put32(ep+4, nameDirOff[idKey{k, r.id}]|0x80000000)
			ep += entry
		}
	}

	var relocs []uint32
	for _, k := range typeKeys {
		for _, r := range types[k] {
			nd := nameDirOff[idKey{k, r.id}]
			writeDirHeader(nd, 1)
			put32(nd+dirHdr, r.lang)
			put32(nd+dirHdr+4, dataEntOff[idKey{k, r.id}])

			de := dataEntOff[idKey{k, r.id}]
			put32(de, blobOff[idKey{k, r.id}])
			put32(de+4, uint32(len(r.data)))
			put32(de+8, 0)
			put32(de+12, 0)
			relocs = append(relocs, de)

			copy(buf[blobOff[idKey{k, r.id}]:], r.data)
		}
	}

	const fileHdr = 20
	const secHdr = 40
	const relSize = 10
	const symSize = 18

	rawPtr := uint32(fileHdr + secHdr)
	relocPtr := rawPtr + sectionSize
	symPtr := relocPtr + uint32(len(relocs))*relSize

	var b bytes.Buffer
	bw := func(v interface{}) { binary.Write(&b, binary.LittleEndian, v) }

	bw(uint16(0x8664))
	bw(uint16(1))
	bw(uint32(0))
	bw(symPtr)
	bw(uint32(1))
	bw(uint16(0))
	bw(uint16(0))

	var name [8]byte
	copy(name[:], ".rsrc")
	b.Write(name[:])
	bw(uint32(0))
	bw(uint32(0))
	bw(sectionSize)
	bw(rawPtr)
	bw(relocPtr)
	bw(uint32(0))
	bw(uint16(len(relocs)))
	bw(uint16(0))
	bw(uint32(0x40000040))

	b.Write(buf)

	for _, at := range relocs {
		bw(at)
		bw(uint32(0))
		bw(uint16(3))
	}

	var sym [8]byte
	copy(sym[:], ".rsrc")
	b.Write(sym[:])
	bw(uint32(0))
	bw(uint16(1))
	bw(uint16(0))
	bw(uint8(3))
	bw(uint8(0))

	bw(uint32(4))

	return b.Bytes()
}
