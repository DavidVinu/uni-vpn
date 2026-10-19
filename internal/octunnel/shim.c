//go:build libopenconnect

/* Callbacks for libopenconnect that behave like the openconnect program (main.c) run with
 * --user, --authgroup, --passwd-on-stdin and --non-inter: the same answers to the login forms
 * and the same output lines. */

#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <unistd.h>
#ifdef __APPLE__
#include <TargetConditionals.h>
#endif

#include "shim.h"
#include "_cgo_export.h"

/* What openssl.c and gnutls.c print right before they ask validate_peer_cert. */
#define VERIFY_FAILED "Server certificate verify failed"

static void output(struct uni_vpn *u, const char *text)
{
	goOutput(u->handle, (char *)text);
}

static char *vformat(const char *fmt, va_list args)
{
	va_list copy;
	char *buf;
	int n;

	va_copy(copy, args);
	n = vsnprintf(NULL, 0, fmt, args);
	buf = n < 0 ? NULL : malloc(n + 1);
	if (buf)
		vsnprintf(buf, n + 1, fmt, copy);
	va_end(copy);
	return buf;
}

static void voutputf(struct uni_vpn *u, const char *fmt, va_list args)
{
	char *buf = vformat(fmt, args);

	if (buf) {
		output(u, buf);
		free(buf);
	}
}

static void __attribute__ ((format(printf, 2, 3)))
outputf(struct uni_vpn *u, const char *fmt, ...)
{
	va_list args;

	va_start(args, fmt);
	voutputf(u, fmt, args);
	va_end(args);
}

static void __attribute__ ((format(printf, 3, 4)))
progress_cb(void *priv, int level, const char *fmt, ...)
{
	struct uni_vpn *u = priv;
	va_list args;
	char *buf;

	(void)level; /* the library already dropped what is above the log level */
	va_start(args, fmt);
	buf = vformat(fmt, args);
	va_end(args);
	if (!buf)
		return;
	/* The daemon reads any line with "certificate" as an untrusted server, so this one waits
	 * for the trust store's verdict (validate_peer_cert_cb). */
	if (u->trust && !strncmp(buf, VERIFY_FAILED, strlen(VERIFY_FAILED))) {
		free(u->cert_line);
		u->cert_line = buf;
		return;
	}
	output(u, buf);
	free(buf);
}

static void wipe(char **s)
{
	if (*s) {
		memset(*s, 0, strlen(*s));
		free(*s);
		*s = NULL;
	}
}

/* prompt_for_input() in non-interactive mode: the next stdin line, if any. */
static char *prompt(struct uni_vpn *u, const char *label)
{
	char *response = u->queued;

	u->queued = NULL;
	if (!response) {
		outputf(u, "%s***\n", label ? label : "");
		output(u, "User input required in non-interactive mode\n");
	}
	return response;
}

/* match_choice_label() of main.c. */
static int match_choice_label(struct uni_vpn *u, struct oc_form_opt_select *select_opt, const char *label)
{
	int i, input_len, partial_matches = 0;
	char *match = NULL;

	input_len = strlen(label);
	if (input_len < 1)
		return -EINVAL;

	for (i = 0; i < select_opt->nr_choices; i++) {
		struct oc_choice *choice = select_opt->choices[i];

		if (!strncasecmp(label, choice->label, input_len)) {
			if ((int)strlen(choice->label) == input_len) {
				select_opt->form._value = choice->name;
				return 0;
			}
			match = choice->name;
			partial_matches++;
		}
	}

	if (partial_matches == 1) {
		select_opt->form._value = match;
		return 0;
	} else if (partial_matches > 1) {
		outputf(u, "Auth choice \"%s\" matches multiple options\n", label);
		return -EINVAL;
	}
	outputf(u, "Auth choice \"%s\" not available\n", label);
	return -EINVAL;
}

/* prompt_opt_select() of main.c: a single choice is taken, else the next stdin line. */
static int prompt_opt_select(struct uni_vpn *u, struct oc_form_opt_select *select_opt, char **saved)
{
	char *response;

	if (!select_opt->nr_choices)
		return -EINVAL;
	for (;;) {
		if (select_opt->nr_choices == 1)
			response = strdup(select_opt->choices[0]->label);
		else
			response = prompt(u, select_opt->form.label);
		if (!response)
			return -EINVAL;
		if (match_choice_label(u, select_opt, response) == 0)
			break;
		free(response);
		if (select_opt->nr_choices == 1)
			return -EINVAL;
	}
	if (saved) {
		free(*saved);
		*saved = response;
	} else {
		free(response);
	}
	return 0;
}

/* process_auth_form_cb() of main.c without --form-entry fields. */
static int auth_form_cb(void *priv, struct oc_auth_form *form)
{
	struct uni_vpn *u = priv;
	struct oc_form_opt *opt;
	int empty = 1;

	if (!form->auth_id)
		return -EINVAL;

	if (form->banner)
		outputf(u, "%s\n", form->banner);
	if (form->error) {
		outputf(u, "%s\n", form->error);
		/* "Login failed.": the program's stdin is closed now (tunnel.go), so a second
		 * password prompt ends there instead of sending "push" or the code as the password. */
		wipe(&u->queued);
	}
	if (form->message)
		outputf(u, "%s\n", form->message);

	if (form->authgroup_opt) {
		if (!u->authgroup || match_choice_label(u, form->authgroup_opt, u->authgroup) != 0) {
			if (prompt_opt_select(u, form->authgroup_opt, &u->authgroup) < 0)
				return OC_FORM_RESULT_ERR;
		}
		if (!u->authgroup_set) {
			u->authgroup_set = 1;
			return OC_FORM_RESULT_NEWGROUP;
		}
	}

	for (opt = form->opts; opt; opt = opt->next) {
		if (opt->flags & OC_FORM_OPT_IGNORE)
			continue;

		if (opt->type == OC_FORM_OPT_SELECT) {
			struct oc_form_opt_select *select_opt = (void *)opt;

			if (select_opt == form->authgroup_opt)
				continue;
			if (prompt_opt_select(u, select_opt, NULL) < 0)
				return OC_FORM_RESULT_ERR;
			empty = 0;
		} else if (opt->type == OC_FORM_OPT_TEXT) {
			if (u->user && (!strncasecmp(opt->name, "user", 4) || !strncasecmp(opt->name, "uname", 5)))
				opt->_value = strdup(u->user);
			else
				opt->_value = prompt(u, opt->label);
			if (!opt->_value)
				return OC_FORM_RESULT_ERR;
			empty = 0;
		} else if (opt->type == OC_FORM_OPT_PASSWORD) {
			if (u->password) {
				opt->_value = u->password;
				u->password = NULL;
			} else {
				opt->_value = prompt(u, opt->label);
			}
			if (!opt->_value)
				return OC_FORM_RESULT_ERR;
			empty = 0;
		} else if (opt->type == OC_FORM_OPT_TOKEN) {
			/* The library fills in the generated code. */
			empty = 0;
		}
	}

	if (!empty)
		u->last_form_empty = 0;
	else if (++u->last_form_empty >= 3) {
		outputf(u, "%d consecutive empty forms, aborting loop\n", u->last_form_empty);
		return OC_FORM_RESULT_CANCELLED;
	}
	return OC_FORM_RESULT_OK;
}

/* The library asks only when its own check against the system's certificates failed. The
 * program then refuses in non-interactive mode; on iOS, where OpenSSL has no certificate
 * file, the phone's trust store decides. */
static int validate_peer_cert_cb(void *priv, const char *reason)
{
	struct uni_vpn *u = priv;
	char *held = u->cert_line;

	u->cert_line = NULL;
	/* The name of the server this connection reached, after any redirect. */
	if (u->trust && u->trust(u->vpninfo, openconnect_get_dnsname(u->vpninfo)) == 0) {
		free(held);
		return 0;
	}
	if (held) {
		output(u, held);
		free(held);
	}
	outputf(u, "\nCertificate from VPN server \"%s\" failed verification.\nReason: %s\n",
		openconnect_get_hostname(u->vpninfo), reason);
	return -EINVAL;
}

static char last_trust_host[256];

static int trust_all(struct openconnect_info *vpninfo, const char *host)
{
	(void)vpninfo;
	snprintf(last_trust_host, sizeof(last_trust_host), "%s", host ? host : "");
	return 0;
}

static int trust_none(struct openconnect_info *vpninfo, const char *host)
{
	trust_all(vpninfo, host);
	return -EINVAL;
}

void uni_vpn_test_trust(struct uni_vpn *u, int accept)
{
	u->trust = accept == 1 ? trust_all : trust_none;
}

const char *uni_vpn_last_trust_host(void)
{
	return last_trust_host;
}

static void reconnected_cb(void *priv)
{
	struct uni_vpn *u = priv;

	goReconnected(u->handle);
}

static char *dup_or_null(const char *s)
{
	return s && *s ? strdup(s) : NULL;
}

#ifdef UNI_VPN_SET_LOGIN
/* Added to libopenconnect by packaging/ios/build-native.sh. */
void uni_vpn_set_login(struct openconnect_info *vpninfo, const char *authgroup, int no_external_auth);
#endif

struct uni_vpn *uni_vpn_new(uintptr_t handle, const char *useragent, const char *user,
			    const char *authgroup, const char *password, const char *queued,
			    int no_external_auth)
{
	struct uni_vpn *u = calloc(1, sizeof(*u));

	if (!u)
		return NULL;
	u->handle = handle;
	u->user = dup_or_null(user);
	u->authgroup = dup_or_null(authgroup);
	/* --passwd-on-stdin reads the first line even when it is empty */
	u->password = password ? strdup(password) : NULL;
	u->queued = dup_or_null(queued);
#if defined(__APPLE__) && TARGET_OS_IPHONE
	u->trust = uni_vpn_trust; /* OpenSSL there has no certificate file */
#endif
	u->vpninfo = openconnect_vpninfo_new(useragent, validate_peer_cert_cb, NULL, auth_form_cb,
					     progress_cb, u);
	if (!u->vpninfo) {
		uni_vpn_free(u);
		return NULL;
	}
	openconnect_set_reconnected_handler(u->vpninfo, reconnected_cb);
#ifdef UNI_VPN_SET_LOGIN
	/* Like the program's --authgroup and --no-external-auth: the first request names the
	 * group and offers no web sign-in, so a gateway whose default group signs in on a web
	 * page answers with the group's password form. */
	uni_vpn_set_login(u->vpninfo, u->authgroup, no_external_auth);
#else
	(void)no_external_auth;
#endif
	return u;
}

void uni_vpn_free(struct uni_vpn *u)
{
	if (!u)
		return;
	if (u->vpninfo)
		openconnect_vpninfo_free(u->vpninfo);
	free(u->user);
	free(u->authgroup);
	wipe(&u->password);
	wipe(&u->queued);
	free(u->cert_line);
	free(u);
}

static void copy(char *dst, int len, const char *src)
{
	if (len > 0) {
		snprintf(dst, len, "%s", src ? src : "");
	}
}

int uni_vpn_ip_info(struct uni_vpn *u, char *addr, int addrlen, char *dns, int dnslen, int *mtu)
{
	const struct oc_ip_info *info = NULL;
	int i, ret = openconnect_get_ip_info(u->vpninfo, &info, NULL, NULL);

	if (ret || !info)
		return ret ? ret : -EINVAL;
	copy(addr, addrlen, info->addr);
	if (dnslen > 0)
		dns[0] = 0;
	for (i = 0; i < 3; i++) {
		size_t used = strlen(dns);

		if (info->dns[i] && dnslen - (int)used > 1)
			snprintf(dns + used, dnslen - used, "%s%s", used ? " " : "", info->dns[i]);
	}
	*mtu = info->mtu;
	return 0;
}

int uni_vpn_write_cmd(int fd, char cmd)
{
	return write(fd, &cmd, 1) == 1 ? 0 : -errno;
}
