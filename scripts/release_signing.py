"""Ed25519 release metadata, compatible with internal/updater's canonical JSON."""
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PUBLIC_KEY_FILE = ROOT / "internal/updater/release.pub"
VERSION = re.compile(r"v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-rc\.(0|[1-9][0-9]{0,8}))?")
FIELDS = ("version", "goos", "goarch", "sha256", "min_proto", "max_proto", "min_state", "max_state", "created_at", "expires_at", "signature")
PUBLIC_DER_PREFIX = bytes.fromhex("302a300506032b6570032100")


def public_key():
    raw = bytes.fromhex(PUBLIC_KEY_FILE.read_text().strip())
    assert len(raw) == 32, "invalid repository signing public key"
    return raw


def validate_private_key(path):
    path = Path(path).absolute()
    info = path.lstat()
    assert stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and info.st_mode & 0o077 == 0, "signing key must be an owner-only regular file"
    assert not path.resolve().is_relative_to(ROOT), "signing private key must remain outside the source tree"
    public = subprocess.check_output(["openssl", "pkey", "-in", str(path), "-pubout", "-outform", "DER"], stderr=subprocess.PIPE)
    assert public == PUBLIC_DER_PREFIX + public_key(), "signing key does not match the published trust root"
    return path


def canonical(manifest):
    assert set(manifest) == set(FIELDS), "unexpected or missing signed field"
    ordered = {field: manifest[field] if field != "signature" else "" for field in FIELDS}
    return json.dumps(ordered, separators=(",", ":"), ensure_ascii=True).encode()


def sign(binary, version, arch, key, created):
    manifest = dict(zip(FIELDS, (version, "linux", arch, hashlib.sha256(binary).hexdigest(), 1, 1, 1, 1,
                                 created.strftime("%Y-%m-%dT%H:%M:%SZ"), (created + timedelta(days=180)).strftime("%Y-%m-%dT%H:%M:%SZ"), "")))
    # pkeyutl's Ed25519 one-shot interface requires a regular input file.
    with tempfile.TemporaryDirectory(prefix="herdrx-release-sign-") as temporary:
        payload = Path(temporary) / "payload.json"
        payload.write_bytes(canonical(manifest))
        signature = subprocess.check_output(["openssl", "pkeyutl", "-sign", "-rawin", "-inkey", str(key), "-in", str(payload)], stderr=subprocess.PIPE)
    assert len(signature) == 64
    manifest["signature"] = signature.hex()
    verify(manifest, binary, version, arch)
    return manifest


def verify(manifest, binary, version, arch):
    payload = canonical(manifest)
    assert VERSION.fullmatch(version) and manifest["version"] == version
    assert manifest["goos"] == "linux" and manifest["goarch"] == arch and arch in ("amd64", "arm64")
    assert manifest["sha256"] == hashlib.sha256(binary).hexdigest()
    assert 0 < len(binary) <= 128 << 20
    assert all(type(manifest[k]) is int and manifest[k] == 1 for k in ("min_proto", "max_proto", "min_state", "max_state"))
    created = datetime.strptime(manifest["created_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    expires = datetime.strptime(manifest["expires_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    now = datetime.now(timezone.utc)
    assert created <= now + timedelta(minutes=5) and expires > now and timedelta(0) < expires - created <= timedelta(days=366)
    signature = bytes.fromhex(manifest["signature"])
    assert len(signature) == 64
    with tempfile.TemporaryDirectory(prefix="herdrx-release-verify-") as temporary:
        root = Path(temporary)
        (root / "public.der").write_bytes(PUBLIC_DER_PREFIX + public_key())
        (root / "payload.json").write_bytes(payload)
        (root / "signature").write_bytes(signature)
        result = subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(root / "public.der"), "-keyform", "DER", "-rawin", "-in", str(root / "payload.json"), "-sigfile", str(root / "signature")], capture_output=True)
        assert result.returncode == 0, "release manifest signature verification failed"
