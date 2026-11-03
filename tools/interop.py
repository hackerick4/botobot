#!/usr/bin/env python3
"""bot-bridge protocol interop checker (PyNaCl), DEV_SPEC §7.

A second, independent implementation of the bb-e2e-v1 envelope used to prove the
Go code is interoperable. NOT a production client. Three subcommands:

  verify-vectors <dir>      Verify every testdata/vectors/*.json: SigInput bytes,
                            signature, ciphertext, binding_sig, fingerprint, and
                            that each sealed wrapped_key opens to K and decrypts
                            to the expected plaintext.
  decrypt <input.json>      Decrypt a Go-produced envelope (§7.8) and print the
                            recovered plaintext as hex on stdout.
  encrypt <params.json>     Encrypt an envelope (§7.4) and print the wire JSON
                            (standard base64 fields) on stdout, for Go to decrypt.

Crypto: XChaCha20-Poly1305 IETF content, crypto_box_seal (SealedBox) key
wrapping, Ed25519 signatures — matching DEV_SPEC §7.1.
"""

import base64
import hashlib
import json
import os
import sys

import nacl.bindings as b
from nacl.public import PrivateKey, PublicKey, SealedBox
from nacl.signing import SigningKey, VerifyKey
from nacl.exceptions import BadSignatureError, CryptoError

PROTOCOL = "bb-e2e-v1"
KEY_BINDING_PREFIX = b"bb-key-v1\n"


def b64e(data: bytes) -> str:
    return base64.b64encode(data).decode("ascii")


def b64d(s: str) -> bytes:
    return base64.b64decode(s)


def sha256(data: bytes) -> bytes:
    return hashlib.sha256(data).digest()


def aad(bridge_id: str, sender_land_id: str, msg_id: str) -> bytes:
    return f"{PROTOCOL}|{bridge_id}|{sender_land_id}|{msg_id}".encode("utf-8")


def public_enc_key(enc_priv: bytes) -> bytes:
    """Derive the X25519 public key from a 32-byte private key."""
    return b.crypto_scalarmult_base(enc_priv)


def sig_input(header: dict) -> bytes:
    """Build the exact §7.5 SigInput bytes from a wire header.

    The wire header already carries nonce, ciphertext_sha256 and each
    wrapped_key_sha256 as standard base64 strings, which is exactly what
    SigInput uses, so they are emitted verbatim. recipients are assumed sorted
    by land_id (the sender sorts them)."""
    lines = [
        header["v"],
        header["bridge_id"],
        header["msg_id"],
        header["sender_land_id"],
        header["sender_key_id"],
        str(header["land_set_version"]),
        header["created_at"],
        header["reply_to"] if header.get("reply_to") else "",
        ",".join(header.get("mentions") or []),
        header["nonce"],
        header["ciphertext_sha256"],
        str(len(header["recipients"])),
    ]
    for r in header["recipients"]:
        lines.append(f'{r["land_id"]}:{r["key_id"]}:{r["wrapped_key_sha256"]}')
    return "".join(line + "\n" for line in lines).encode("utf-8")


def fingerprint(sign_pub: bytes, enc_pub: bytes) -> str:
    d = sha256(sign_pub + enc_pub)
    groups = []
    for i in range(6):
        g = d[i * 5 : i * 5 + 5]
        v = int.from_bytes(g, "big")
        groups.append("%05d" % (v % 100000))
    return " ".join(groups)


def binding_input(enc_pub: bytes, sign_pub: bytes) -> bytes:
    return KEY_BINDING_PREFIX + enc_pub + sign_pub


# ---------------------------------------------------------------------------
# verify-vectors
# ---------------------------------------------------------------------------

def verify_vector(path: str) -> None:
    with open(path, "r", encoding="utf-8") as f:
        v = json.load(f)

    sender = v["sender"]
    seed = bytes.fromhex(sender["sign_seed_hex"])
    sk = SigningKey(seed)
    sign_pub = bytes(sk.verify_key)
    if sign_pub.hex() != sender["sign_pub_hex"]:
        raise AssertionError("sender sign_pub mismatch")

    enc_priv = bytes.fromhex(sender["enc_priv_hex"])
    enc_pub = public_enc_key(enc_priv)
    if enc_pub.hex() != sender["enc_pub_hex"]:
        raise AssertionError("sender enc_pub mismatch")

    # Key binding (§7.6).
    VerifyKey(sign_pub).verify(
        binding_input(enc_pub, sign_pub), bytes.fromhex(sender["binding_sig_hex"])
    )

    # Fingerprint (§7.7).
    if fingerprint(sign_pub, enc_pub) != sender["fingerprint"]:
        raise AssertionError("fingerprint mismatch")

    env = v["envelope"]
    header = env["header"]

    # SigInput exact bytes (§7.5).
    si = sig_input(header)
    if si.hex() != v["sig_input_hex"]:
        raise AssertionError(
            f"sig_input mismatch:\n got {si.hex()}\nwant {v['sig_input_hex']}"
        )

    # Signature (§7.8 step 2).
    signature = b64d(env["signature"])
    if signature.hex() != v["signature_hex"]:
        raise AssertionError("signature bytes mismatch")
    VerifyKey(sign_pub).verify(si, signature)

    # Ciphertext is deterministic given K, nonce, plaintext, AAD (§7.4).
    K = bytes.fromhex(v["k_hex"])
    nonce = bytes.fromhex(v["nonce_hex"])
    plaintext = bytes.fromhex(v["plaintext_hex"])
    ad = aad(header["bridge_id"], header["sender_land_id"], header["msg_id"])
    recomputed = b.crypto_aead_xchacha20poly1305_ietf_encrypt(plaintext, ad, nonce, K)
    ciphertext = b64d(env["ciphertext"])
    if recomputed != ciphertext:
        raise AssertionError("ciphertext mismatch")
    if sha256(ciphertext) != b64d(header["ciphertext_sha256"]):
        raise AssertionError("ciphertext_sha256 mismatch")

    # Each recipient: open sealed key -> K, decrypt -> plaintext (§7.8 steps 3-5).
    for r in v["recipients"]:
        r_priv = bytes.fromhex(r["enc_priv_hex"])
        wrapped = b64d(env["wrapped_keys"][r["land_id"]])
        if len(wrapped) != 80:
            raise AssertionError("wrapped_key length != 80")
        # match the recipient's header line hash
        hr = next(x for x in header["recipients"] if x["land_id"] == r["land_id"])
        if sha256(wrapped) != b64d(hr["wrapped_key_sha256"]):
            raise AssertionError("wrapped_key_sha256 mismatch")
        opened = SealedBox(PrivateKey(r_priv)).decrypt(wrapped)
        if opened != K:
            raise AssertionError("unwrapped K mismatch")
        got = b.crypto_aead_xchacha20poly1305_ietf_decrypt(
            ciphertext, ad, b64d(header["nonce"]), opened
        )
        if got != plaintext:
            raise AssertionError("decrypted plaintext mismatch")


def cmd_verify_vectors(dir_path: str) -> int:
    paths = sorted(
        os.path.join(dir_path, p)
        for p in os.listdir(dir_path)
        if p.endswith(".json")
    )
    if not paths:
        print(f"no vectors in {dir_path}", file=sys.stderr)
        return 1
    for p in paths:
        try:
            verify_vector(p)
        except (AssertionError, BadSignatureError, CryptoError) as e:
            print(f"FAIL {os.path.basename(p)}: {e}", file=sys.stderr)
            return 1
        print(f"OK {os.path.basename(p)}")
    return 0


# ---------------------------------------------------------------------------
# decrypt (Go envelope -> plaintext)
# ---------------------------------------------------------------------------

def cmd_decrypt(input_path: str) -> int:
    """input.json: {envelope, my_land_id, my_key_id, my_enc_priv_hex,
    sender_sign_pub_hex}. Prints plaintext hex on success."""
    with open(input_path, "r", encoding="utf-8") as f:
        inp = json.load(f)

    env = inp["envelope"]
    header = env["header"]
    my_land_id = inp["my_land_id"]
    my_key_id = inp["my_key_id"]
    my_enc_priv = bytes.fromhex(inp["my_enc_priv_hex"])
    sender_sign_pub = bytes.fromhex(inp["sender_sign_pub_hex"])

    # §7.8 step 2.
    VerifyKey(sender_sign_pub).verify(sig_input(header), b64d(env["signature"]))

    # step 3.
    hr = next((x for x in header["recipients"] if x["land_id"] == my_land_id), None)
    if hr is None:
        raise AssertionError("not_recipient")
    wrapped = b64d(env["wrapped_keys"][my_land_id])
    if sha256(wrapped) != b64d(hr["wrapped_key_sha256"]):
        raise AssertionError("wrapped_key_mismatch")
    if hr["key_id"] != my_key_id:
        raise AssertionError("unknown_recipient_key")

    # step 4.
    ciphertext = b64d(env["ciphertext"])
    if sha256(ciphertext) != b64d(header["ciphertext_sha256"]):
        raise AssertionError("ciphertext_hash_mismatch")

    # step 5.
    K = SealedBox(PrivateKey(my_enc_priv)).decrypt(wrapped)
    ad = aad(header["bridge_id"], header["sender_land_id"], header["msg_id"])
    plaintext = b.crypto_aead_xchacha20poly1305_ietf_decrypt(
        ciphertext, ad, b64d(header["nonce"]), K
    )
    sys.stdout.write(plaintext.hex())
    return 0


# ---------------------------------------------------------------------------
# encrypt (params -> Go-decryptable envelope)
# ---------------------------------------------------------------------------

def cmd_encrypt(params_path: str) -> int:
    with open(params_path, "r", encoding="utf-8") as f:
        p = json.load(f)

    sk = SigningKey(bytes.fromhex(p["sender_sign_seed_hex"]))
    sign_pub = bytes(sk.verify_key)

    plaintext = bytes.fromhex(p["plaintext_hex"])
    K = os.urandom(32)
    nonce = os.urandom(24)
    ad = aad(p["bridge_id"], p["sender_land_id"], p["msg_id"])
    ciphertext = b.crypto_aead_xchacha20poly1305_ietf_encrypt(plaintext, ad, nonce, K)

    recips = sorted(p["recipients"], key=lambda r: r["land_id"])
    header_recips = []
    wrapped_keys = {}
    for r in recips:
        enc_pub = bytes.fromhex(r["enc_pub_hex"])
        wrapped = SealedBox(PublicKey(enc_pub)).encrypt(K)
        wrapped_keys[r["land_id"]] = b64e(wrapped)
        header_recips.append(
            {
                "land_id": r["land_id"],
                "key_id": r["key_id"],
                "wrapped_key_sha256": b64e(sha256(wrapped)),
            }
        )

    header = {
        "v": PROTOCOL,
        "bridge_id": p["bridge_id"],
        "msg_id": p["msg_id"],
        "sender_land_id": p["sender_land_id"],
        "sender_key_id": p["sender_key_id"],
        "land_set_version": p["land_set_version"],
        "created_at": p["created_at"],
        "mentions": p.get("mentions"),
        "reply_to": p.get("reply_to"),
        "nonce": b64e(nonce),
        "ciphertext_sha256": b64e(sha256(ciphertext)),
        "recipients": header_recips,
    }
    signature = sk.sign(sig_input(header)).signature

    envelope = {
        "header": header,
        "signature": b64e(signature),
        "ciphertext": b64e(ciphertext),
        "wrapped_keys": wrapped_keys,
    }
    # sign_pub echoed so the caller can verify without re-deriving it.
    json.dump({"envelope": envelope, "sender_sign_pub_hex": sign_pub.hex()}, sys.stdout)
    return 0


def main(argv) -> int:
    if len(argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    cmd = argv[1]
    if cmd == "verify-vectors" and len(argv) == 3:
        return cmd_verify_vectors(argv[2])
    if cmd == "decrypt" and len(argv) == 3:
        return cmd_decrypt(argv[2])
    if cmd == "encrypt" and len(argv) == 3:
        return cmd_encrypt(argv[2])
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
