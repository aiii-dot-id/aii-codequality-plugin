//go:build tinygo.wasm

package cq

import "unsafe"

const lowHeap = 5 << 18

var ballast [][]byte

//export llvm.wasm.memory.size.i32
func wasmMemorySize(index int32) int32

//export llvm.wasm.memory.grow.i32
func wasmMemoryGrow(index int32, delta int32) int32

func alignHeapLadder() {
	n := wasmMemorySize(0)
	p := int32(1)
	for p < n {
		p <<= 1
	}
	if p > n {
		wasmMemoryGrow(0, p-n)
	}
}

func FillLowHeap() {
	if ballast != nil {
		return
	}
	alignHeapLadder()
	for _, size := range []int{64 << 10, 4 << 10, 256, 16} {
		for {
			b := make([]byte, size)
			if uintptr(unsafe.Pointer(&b[0])) >= lowHeap {
				break
			}
			ballast = append(ballast, b)
		}
	}
}
