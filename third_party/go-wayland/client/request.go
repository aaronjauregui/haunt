package client

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"
)

// HAUNT PATCH: HAUNT_WAYLAND_DEBUG=1 traces outgoing requests.
var debugWire = os.Getenv("HAUNT_WAYLAND_DEBUG") != ""

func (ctx *Context) WriteMsg(b []byte, oob []byte) error {
	// HAUNT PATCH: serialize concurrent request writers.
	ctx.writeMu.Lock()
	defer ctx.writeMu.Unlock()
	if debugWire && len(b) >= 8 {
		sender := binary.LittleEndian.Uint32(b[0:4])
		h := binary.LittleEndian.Uint32(b[4:8])
		fmt.Fprintf(os.Stderr, "[wire] -> id=%d opcode=%d len=%d oob=%d\n", sender, h&0xffff, h>>16, len(oob))
	}
	n, oobn, err := ctx.conn.WriteMsgUnix(b, oob, nil)
	if err != nil {
		return err
	}
	if n != len(b) || oobn != len(oob) {
		return fmt.Errorf("ctx.WriteMsg: incorrect number of bytes written (n=%d oobn=%d)", n, oobn)
	}

	return nil
}

func PutUint32(dst []byte, v uint32) {
	_ = dst[3]
	*(*uint32)(unsafe.Pointer(&dst[0])) = v
}

func PutFixed(dst []byte, f float64) {
	fx := fixedFromfloat64(f)
	_ = dst[3]
	*(*int32)(unsafe.Pointer(&dst[0])) = fx
}

func PutString(dst []byte, v string, l int) {
	PutUint32(dst[:4], uint32(l))
	copy(dst[4:], []byte(v))
}

func PutArray(dst []byte, a []byte) {
	PutUint32(dst[:4], uint32(len(a)))
	copy(dst[4:], a)
}
