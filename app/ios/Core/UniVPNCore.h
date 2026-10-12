// The Go core (cmd/uni-vpn-ios, built by packaging/ios/build-core.sh) as the app and the
// tunnel extension see it. Go's own generated header is not used: it carries Go's types,
// which Swift does not need. build-core.sh checks that these match it.
#ifndef UNIVPN_CORE_H
#define UNIVPN_CORE_H

// Starts the daemon in this process: 0 started, 1 another process holds the lock (after
// waiting lockWaitMs), 2 error. All arguments are C strings; the core copies them.
int UniVPNStart(char *configHome, char *stateHome, char *apiToken, char *secretsKeyHex, char *deviceName, char *lockWaitMs);

// Stops the daemon. Returns within about 5 seconds with the lock and the ports released, so
// the other process can start its daemon right away; a daemon slow to end (a tunnel saying
// goodbye to the gateway) finishes in the background.
void UniVPNStop(void);

// 1 while the daemon runs in this process.
int UniVPNRunning(void);

#endif
