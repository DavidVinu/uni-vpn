#ifndef UNI_VPN_SHIM_H
#define UNI_VPN_SHIM_H

#include <stdint.h>
#include <openconnect.h>

/* One libopenconnect session and what the openconnect program would have read from its
 * command line and stdin. */
struct uni_vpn {
	struct openconnect_info *vpninfo;
	uintptr_t handle; /* cgo.Handle of the Go session */
	char *user;
	char *authgroup;
	char *password;	 /* the first stdin line, taken by the first password field */
	char *queued;	 /* the second stdin line, taken by the next prompt */
	char *host;	 /* the gateway's name, for the certificate check on iOS */
	int authgroup_set;
	int last_form_empty;
};

struct uni_vpn *uni_vpn_new(uintptr_t handle, const char *useragent, const char *user,
			    const char *authgroup, const char *password, const char *queued,
			    const char *host);
void uni_vpn_free(struct uni_vpn *u);
int uni_vpn_ip_info(struct uni_vpn *u, char *addr, int addrlen, char *dns, int dnslen, int *mtu);
int uni_vpn_write_cmd(int fd, char cmd);

/* Defined on iOS (trust_ios.c): 0 when the phone's trust store accepts the server's chain. */
int uni_vpn_trust(struct openconnect_info *vpninfo, const char *host);

#endif
