"""Check the portable layout and native binaries against the release contract."""
import os
from pathlib import Path
import re
import subprocess
import sys
from zipfile import ZipFile

out = Path(sys.argv[1])
version = Path('VERSION').read_text().strip()
public_key = Path('scripts/release-public-key.txt').read_bytes().strip()
installer = out / f'Meerkat-Setup-{version}.exe'
apk = out / f'Meerkat-Android-{version}.apk'
with installer.open('rb') as stream:
    assert stream.read(2) == b'MZ', 'Expected Windows installer'
with ZipFile(out / f'Meerkat-Windows-v{version}.zip') as portable:
    files = sorted(entry.filename for entry in portable.infolist() if not entry.is_dir())
    assert files == ['Meerkat-Windows/Meerkat.exe', 'Meerkat-Windows/meerkat-edge.exe'], files
    for name in files:
        binary = portable.read(name)  # Also checks the ZIP CRC.
        assert binary[:2] == b'MZ', name
        if name.endswith('/Meerkat.exe'):
            assert public_key in binary, 'Desktop update key missing'
with ZipFile(apk) as package:
    assert public_key in package.read('lib/arm64-v8a/libwails.so'), 'Android update key missing'

tools = Path(os.environ['ANDROID_HOME']) / 'build-tools/35.0.0'
certs = subprocess.check_output([tools / 'apksigner', 'verify', '--print-certs', apk], text=True)
# Signer of the published v0.2.6 APK. Require exactly this signer.
assert re.findall(r'^Signer #\d+ certificate SHA-256 digest: (\w+)$', certs, re.M) == [
    '190af4deaa53668445ccfe27e7fdbfb509c142d80c8b767172ea48040737cc5d'
]
badging = subprocess.check_output([tools / 'aapt', 'dump', 'badging', apk], text=True)
assert re.search(r"^native-code: 'arm64-v8a'\s*$", badging, re.M), 'Unexpected APK architecture'
print(f'Verified Windows packages, Android signer, ABI and embedded update keys for {version}')
