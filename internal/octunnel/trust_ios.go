//go:build libopenconnect && ios

package octunnel

/*
#include "shim.h"
*/
import "C"

import (
	"encoding/pem"
	"unsafe"

	"github.com/DavidVinu/uni-vpn/internal/certs"
)

// The phone's trust store checks the gateway (trust_ios.c). It gets internal/certs'
// certificates too, as openconnect's --cafile does on the desktop and on Android.
func init() {
	for rest := certs.PEM; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			return
		}
		if block.Type == "CERTIFICATE" && len(block.Bytes) > 0 {
			C.uni_vpn_trust_add(unsafe.Pointer(&block.Bytes[0]), C.size_t(len(block.Bytes)))
		}
	}
}
