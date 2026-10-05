"""Probe a Cisco AnyConnect gateway without credentials: group list, login fields, SAML.

Sends the XML init request openconnect sends first and reads the login form the server
answers with. Nothing that identifies the user is sent. The result prefills the setup
assistant for a university that is not in universities.json.

Measured on 2026-10-05 against eleven gateways: every server names
<auth-method>single-sign-on-v2</auth-method> in <opaque> once the client advertises SSO, even
with a password form, so only <sso-v2-login> or an "sso" input means SAML. Load balancers
answer the POST with a 302 to a cluster member, which must get the same POST again.
"""

from __future__ import annotations

import ssl
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from dataclasses import asdict, dataclass, field
from xml.sax.saxutils import escape

from . import universities as unis

INIT_REQUEST = (
    '<?xml version="1.0" encoding="UTF-8"?>\n'
    '<config-auth client="vpn" type="init" aggregate-auth-version="2">'
    '<version who="vpn">v9.12</version><device-id>linux-64</device-id>'
    '{group_select}<group-access>{url}</group-access>'
    '<capabilities><auth-method>single-sign-on-v2</auth-method></capabilities></config-auth>'
)
MAX_REPLY = 256 * 1024
MAX_REDIRECTS = 3
NOT_CISCO = "This address does not answer like a Cisco AnyConnect gateway"


@dataclass
class Detection:
    host: str
    usergroup: str = ""
    reachable: bool = False
    cisco: bool = False
    saml: bool = False
    groups: list[str] = field(default_factory=list)
    group: str = ""  # the group this form belongs to: the selected option, else the group alias
    fields: list[dict] = field(default_factory=list)  # {"name", "type", "label"} of text and password inputs
    second_password: bool = False
    message: str = ""
    error: str = ""

    def suggestion(self) -> dict:
        """Profile values for the setup assistant. The second factor is a guess: a second password
        field is usually TOTP, but without one the server may still want an appended code, Duo,
        or show the OTP field only after the password (Heidelberg)."""
        if self.saml:
            mfa = "saml"
        elif self.second_password:
            mfa = "totp_field"
        else:
            mfa = "none"
        return {"host": self.host, "usergroup": self.usergroup,
                "authgroup": self.group if len(self.groups) > 1 else "", "mfa": mfa}

    def as_dict(self) -> dict:
        data = asdict(self)
        data["suggestion"] = self.suggestion()
        return data


def split_address(text: str) -> tuple[str, str]:
    """"https://vpn.example.edu/staff" -> ("vpn.example.edu", "staff"). Raises unis.FieldError."""
    value = str(text or "").strip()
    for prefix in ("https://", "http://"):
        if value.lower().startswith(prefix):
            value = value[len(prefix):]
    host, _, path = value.partition("/")
    return unis.check_field("host", host), unis.check_field("usergroup", path)


def init_request(host: str, usergroup: str = "", group: str = "") -> bytes:
    url = f"https://{host}/{usergroup}"
    group_select = f"<group-select>{escape(group)}</group-select>" if group else ""
    return INIT_REQUEST.format(group_select=group_select, url=escape(url)).encode("utf-8")


def parse_reply(data: bytes, host: str, usergroup: str = "") -> Detection:
    result = Detection(host=host, usergroup=usergroup, reachable=True)
    # No DTDs: a gateway never sends one, and it is the way to entity expansion attacks.
    if len(data) > MAX_REPLY or b"<!DOCTYPE" in data[:4096].upper():
        result.error = NOT_CISCO
        return result
    try:
        root = ET.fromstring(data)
    except ET.ParseError:
        result.error = NOT_CISCO
        return result
    if root.tag != "config-auth":
        result.error = NOT_CISCO
        return result
    result.cisco = True
    auth = root.find("auth")
    if auth is None:
        result.error = "The gateway sent no login form"
        return result
    result.message = (auth.findtext("message") or "").strip()
    result.saml = auth.find("sso-v2-login") is not None or any(i.get("type") == "sso" for i in auth.iter("input"))
    form = auth.find("form")
    if form is not None:
        for item in form.findall("input"):
            if item.get("type") in ("text", "password"):
                result.fields.append({"name": item.get("name", ""), "type": item.get("type"),
                                      "label": item.get("label", "")})
        for select in form.findall("select"):
            if select.get("name") != "group_list":
                continue
            for option in select.findall("option"):
                name = (option.text or "").strip()
                if not name:
                    continue
                result.groups.append(name)
                if option.get("selected") == "true":
                    result.group = name
    if not result.group:
        result.group = (root.findtext("opaque/group-alias") or "").strip()
    result.second_password = any(f["name"] == "secondary_password" for f in result.fields)
    return result


class _RepostRedirects(urllib.request.HTTPRedirectHandler):
    """urllib turns a redirected POST into a GET without body; the gateway needs the POST again."""

    max_redirections = MAX_REDIRECTS

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if code not in (301, 302, 303, 307, 308) or not newurl.lower().startswith("https://"):
            return None
        # req.headers holds only the headers set by us; Host and Content-Length are recomputed.
        return urllib.request.Request(newurl, data=req.data, headers=dict(req.headers), method="POST")


def default_opener(request: urllib.request.Request, timeout: float) -> bytes:
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl.create_default_context()),
                                         _RepostRedirects)
    with opener.open(request, timeout=timeout) as response:
        return response.read(MAX_REPLY + 1)


def probe(host: str, usergroup: str = "", group: str = "", *, useragent: str = unis.DEFAULT_USERAGENT,
          opener=default_opener, timeout: float = 10) -> Detection:
    """Ask the gateway for its login form. Raises unis.FieldError for an invalid address or group;
    network trouble ends up in Detection.error."""
    host = unis.check_field("host", host)
    usergroup = unis.check_field("usergroup", usergroup)
    group = unis.check_field("authgroup", group)
    request = urllib.request.Request(
        f"https://{host}/{usergroup}", data=init_request(host, usergroup, group), method="POST",
        headers={"User-Agent": useragent, "X-Transcend-Version": "1", "X-Aggregate-Auth": "1",
                 "X-Support-HTTP-Auth": "true", "Accept": "*/*", "Content-Type": "application/x-www-form-urlencoded"})
    try:
        data = opener(request, timeout)
    except urllib.error.HTTPError as exc:
        return Detection(host=host, usergroup=usergroup, reachable=True, error=f"{NOT_CISCO} (HTTP {exc.code})")
    except (urllib.error.URLError, OSError) as exc:
        reason = getattr(exc, "reason", None) or exc
        return Detection(host=host, usergroup=usergroup, error=f"Could not reach {host}: {reason}")
    return parse_reply(data, host, usergroup)
