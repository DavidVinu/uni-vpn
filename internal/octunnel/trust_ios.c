//go:build libopenconnect && ios

/* OpenSSL on iOS has no certificate file, so the library's own check always fails and asks
 * validate_peer_cert. This checks the server's chain with the phone's own trust store, for
 * the gateway's host name, as Safari would. */

#include <errno.h>
#include <stdbool.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

#include "shim.h"

int uni_vpn_trust(struct openconnect_info *vpninfo, const char *host)
{
	struct oc_cert *chain = NULL;
	CFMutableArrayRef certs;
	CFStringRef name;
	SecPolicyRef policy;
	SecTrustRef trust = NULL;
	bool ok = false;
	int i, n;

	n = openconnect_get_peer_cert_chain(vpninfo, &chain);
	if (n <= 0)
		return -EINVAL;
	certs = CFArrayCreateMutable(NULL, n, &kCFTypeArrayCallBacks);
	for (i = 0; i < n; i++) {
		CFDataRef der = CFDataCreate(NULL, chain[i].der_data, chain[i].der_len);
		SecCertificateRef cert = der ? SecCertificateCreateWithData(NULL, der) : NULL;

		if (cert) {
			CFArrayAppendValue(certs, cert);
			CFRelease(cert);
		}
		if (der)
			CFRelease(der);
	}
	openconnect_free_peer_cert_chain(vpninfo, chain);

	name = CFStringCreateWithCString(NULL, host, kCFStringEncodingUTF8);
	policy = SecPolicyCreateSSL(true, name);
	if (CFArrayGetCount(certs) > 0 &&
	    SecTrustCreateWithCertificates(certs, policy, &trust) == errSecSuccess)
		ok = SecTrustEvaluateWithError(trust, NULL);
	if (trust)
		CFRelease(trust);
	CFRelease(policy);
	if (name)
		CFRelease(name);
	CFRelease(certs);
	return ok ? 0 : -EINVAL;
}
