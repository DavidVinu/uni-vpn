import unittest

from uni_vpn import totp

# RFC 6238, Anhang B: Secret "12345678901234567890" (SHA1) bzw. 32 Byte fuer SHA256.
RFC_SHA1 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
RFC_SHA256 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQGEZA"


class NormalizeTests(unittest.TestCase):
    def test_plain_base32_becomes_openconnect_token(self):
        self.assertEqual(totp.normalize(RFC_SHA1), f"base32:{RFC_SHA1}")

    def test_lowercase_spaces_dashes_and_padding_are_cleaned(self):
        messy = " gezd gnbv-gy3t qojq\tGEZD GNBV GY3T QOJQ== "
        self.assertEqual(totp.normalize(messy), f"base32:{RFC_SHA1}")

    def test_otpauth_uri_secret_is_extracted(self):
        uri = f"otpauth://totp/Uni%20Heidelberg:ab123?secret={RFC_SHA1}&issuer=Uni%20Heidelberg&digits=6&period=30"
        self.assertEqual(totp.normalize(uri), f"base32:{RFC_SHA1}")

    def test_otpauth_uri_with_sha256_keeps_algorithm(self):
        uri = f"otpauth://totp/x?secret={RFC_SHA256}&algorithm=SHA256"
        self.assertEqual(totp.normalize(uri), f"sha256:base32:{RFC_SHA256}")

    def test_rejects_hotp_uri(self):
        with self.assertRaises(ValueError) as ctx:
            totp.normalize(f"otpauth://hotp/x?secret={RFC_SHA1}&counter=0")
        self.assertIn("zeitbasiert", str(ctx.exception))

    def test_rejects_unusual_digits_or_period(self):
        for query in ("digits=8", "period=60"):
            with self.assertRaises(ValueError, msg=query):
                totp.normalize(f"otpauth://totp/x?secret={RFC_SHA1}&{query}")

    def test_rejects_unknown_algorithm(self):
        with self.assertRaises(ValueError):
            totp.normalize(f"otpauth://totp/x?secret={RFC_SHA1}&algorithm=MD5")

    def test_rejects_empty_uri_secret(self):
        with self.assertRaises(ValueError):
            totp.normalize("otpauth://totp/x?issuer=y")

    def test_rejects_garbage(self):
        for text in ("", "   ", "0189", "GEZD GNBV 1!", "abc"):
            with self.assertRaises(ValueError, msg=repr(text)):
                totp.normalize(text)

    def test_rejects_newline(self):
        with self.assertRaises(ValueError):
            totp.normalize(f"{RFC_SHA1}\nbase32:AAAA")


class CodeTests(unittest.TestCase):
    def test_rfc6238_sha1_vectors(self):
        token = f"base32:{RFC_SHA1}"
        self.assertEqual(totp.code(token, now=59), "287082")
        self.assertEqual(totp.code(token, now=1111111109), "081804")
        self.assertEqual(totp.code(token, now=1234567890), "005924")

    def test_rfc6238_sha256_vector(self):
        self.assertEqual(totp.code(f"sha256:base32:{RFC_SHA256}", now=59), "119246")

    def test_code_uses_current_time_by_default(self):
        self.assertRegex(totp.code(f"base32:{RFC_SHA1}"), r"^[0-9]{6}$")
