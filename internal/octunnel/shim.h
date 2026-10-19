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
	int authgroup_set;
	int last_form_empty;
	/* Where the library's own certificate check is expected to fail (iOS), its "Server
	 * certificate verify failed" waits here until trust has decided. */
	int (*trust)(struct openconnect_info *vpninfo, const char *host);
	char *cert_line;
};

struct uni_vpn *uni_vpn_new(uintptr_t handle, const char *useragent, const char *user,
			    const char *authgroup, const char *password, const char *queued,
			    int no_external_auth);
void uni_vpn_free(struct uni_vpn *u);
int uni_vpn_ip_info(struct uni_vpn *u, char *addr, int addrlen, char *dns, int dnslen, int *mtu);
int uni_vpn_write_cmd(int fd, char cmd);

/* Tests on Linux: a trust store that accepts (1) or refuses (2) every server. */
void uni_vpn_test_trust(struct uni_vpn *u, int accept);
const char *uni_vpn_last_trust_host(void);

/* Defined on iOS (trust_ios.c): 0 when the phone's trust store accepts the server's chain. */
int uni_vpn_trust(struct openconnect_info *vpninfo, const char *host);

#endif
