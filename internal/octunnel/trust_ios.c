//go:build libopenconnect && ios

/* OpenSSL on iOS has no certificate file, so the library's own check always fails and asks
 * validate_peer_cert. This checks the server's chain with the phone's own trust store, for
 * the gateway's host name, as Safari would. */

#include <errno.h>
#include <stdbool.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <openssl/crypto.h>

#include "shim.h"

/* internal/certs' certificates (trust_ios.go), offered as intermediates after the server's
 * chain: Ohio State's ends at emSign Root TLS CA - G1, which only its cross-certificate links
 * to a root the phone trusts. Filled once before the first connection, then only read. */
static CFMutableArrayRef extra;

void uni_vpn_trust_add(const void *der, size_t len)
{
	CFDataRef data = CFDataCreate(NULL, der, (CFIndex)len);
	SecCertificateRef cert = data ? SecCertificateCreateWithData(NULL, data) : NULL;

	if (cert) {
		if (!extra)
			extra = CFArrayCreateMutable(NULL, 0, &kCFTypeArrayCallBacks);
		if (extra)
			CFArrayAppendValue(extra, cert);
		CFRelease(cert);
	}
	if (data)
		CFRelease(data);
}

int uni_vpn_trust(struct openconnect_info *vpninfo, const char *host)
{
	struct oc_cert *chain = NULL;
	CFMutableArrayRef certs;
	CFStringRef name;
	SecPolicyRef policy;
	SecTrustRef trust = NULL;
	bool ok = false;
	int i, n;

	/* Without a name SecPolicyCreateSSL would accept a certificate for any server. */
	if (!host || !*host)
		return -EINVAL;
	name = CFStringCreateWithCString(NULL, host, kCFStringEncodingUTF8);
	if (!name)
		return -EINVAL;
	n = openconnect_get_peer_cert_chain(vpninfo, &chain);
	if (n <= 0) {
		CFRelease(name);
		return -EINVAL;
	}
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
	/* With OpenSSL the library frees only the array (openssl.c never sets cert_list_size, which
	 * its free uses): free each certificate here, else every connection leaks a chain. NULL
	 * keeps a fixed library from freeing them twice. */
	for (i = 0; i < n; i++) {
		OPENSSL_free(chain[i].der_data);
		chain[i].der_data = NULL;
	}
	openconnect_free_peer_cert_chain(vpninfo, chain);

	/* After the server's own: the first certificate must stay the server's. */
	if (extra && CFArrayGetCount(certs) > 0)
		CFArrayAppendArray(certs, extra, CFRangeMake(0, CFArrayGetCount(extra)));
	policy = SecPolicyCreateSSL(true, name);
	if (CFArrayGetCount(certs) > 0 &&
	    SecTrustCreateWithCertificates(certs, policy, &trust) == errSecSuccess)
		ok = SecTrustEvaluateWithError(trust, NULL);
	if (trust)
		CFRelease(trust);
	CFRelease(policy);
	CFRelease(name);
	CFRelease(certs);
	return ok ? 0 : -EINVAL;
}
