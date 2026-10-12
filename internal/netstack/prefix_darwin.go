package netstack

// openconnect built for macOS or iOS reads and writes its tun fd with the address family in
// front of each packet (TUN_HAS_AF_PREFIX in tun.c), as the utun driver does. Also GOOS=ios.
const (
	afPrefix = true
	afInet   = 2
	afInet6  = 30
)
