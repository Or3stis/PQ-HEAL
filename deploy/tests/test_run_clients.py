#!/usr/bin/env python3
"""Exercise launcher lifecycle with fake clients, without Ubuntu or networking."""
import csv
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time

SCRIPT = Path(__file__).resolve().parents[1] / 'ubuntu22-arm64/scripts/run-clients.sh'


def await_rows(root, count, process):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        files = list((root / 'results').glob('*/clients.tsv'))
        if files:
            with files[0].open() as f:
                rows = list(csv.DictReader(f, delimiter='\t'))
            if len(rows) == count and all((files[0].parent / r['log']).exists() and (files[0].parent / r['log']).read_text().startswith('{') for r in rows):
                return files[0].parent, rows
        if process.poll() is not None:
            raise AssertionError(process.communicate()[0])
        time.sleep(.05)
    raise AssertionError('clients did not start')


def exercise(stop_signal=None, fail_child=False):
    with tempfile.TemporaryDirectory(prefix='pqheal-client-group-') as tmp:
        root = Path(tmp)
        for d in ('scripts', 'bin', 'config'):
            (root / d).mkdir()
        (root / 'scripts/run-clients.sh').write_bytes(SCRIPT.read_bytes())
        # Only platform/config-permission checks are mocked; actual launcher,
        # subprocesses, logs, signals and cleanup execute on the host.
        (root / 'scripts/common.sh').write_text('''set -euo pipefail
umask 077
PACKAGE_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fail() { echo "$*" >&2; exit 1; }
require_target() { :; }
client_config() { set -a; source "$1"; set +a; }
''')
        (root / 'config/custom.env').write_text('''PQHEAL_API_KEY=test-secret-not-an-argument
PQHEAL_SERVER_URL=https://test.invalid:9000
PQHEAL_KEY_PIN=trusted-test-pin
PQHEAL_SENSOR_TYPE=motion
PQHEAL_FREQUENCY=2s
''')
        stub = root / 'bin/pq-heal-client'
        stub.write_text('''#!/usr/bin/env python3
import json,os,signal,sys,time
print(json.dumps({'args':sys.argv[1:], 'has_key':bool(os.getenv('PQHEAL_API_KEY'))}),flush=True)
def stop(*args):
 print('stopped',flush=True)
 sys.exit(0)
signal.signal(signal.SIGTERM,stop)
if os.getenv('FAIL_CHILD') and any(x.startswith('-id=') and x.endswith('-2') for x in sys.argv):
 time.sleep(.2)
 sys.exit(7)
while True: time.sleep(.1)
''')
        stub.chmod(0o755)
        env = dict(os.environ, FAIL_CHILD='1' if fail_child else '')
        for args in (['-0'], ['-1001'], ['-no'], ['-n'], ['-01']):
            result = subprocess.run(['bash', str(root/'scripts/run-clients.sh'), *args], env=env, capture_output=True)
            assert result.returncode != 0, args
        command = ['bash', str(root/'scripts/run-clients.sh')]
        command += ['-n', '3'] if fail_child else ['-3']
        command += [str(root/'config/custom.env')]
        p = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, start_new_session=True)
        rows = []
        try:
            run, rows = await_rows(root, 3, p)
            assert len({r['client_id'] for r in rows}) == 3
            assert len({r['subject_id'] for r in rows}) == 3
            for row in rows:
                path = run / row['log']
                entry = json.loads(path.read_text().splitlines()[0])
                assert entry['has_key']
                assert '-encrypt' in entry['args'] and '-type=motion' in entry['args'] and '-frequency=2s' in entry['args']
                assert not any('test-secret' in x for x in entry['args'])
                assert path.stat().st_mode & 0o777 == 0o600
            if stop_signal:
                p.send_signal(stop_signal)
            output, _ = p.communicate(timeout=10)
            expected = 1 if fail_child else 128 + stop_signal
            assert p.returncode == expected, (p.returncode, output)
            assert int((run/'EXIT_STATUS').read_text()) == expected
            for row in rows:
                try:
                    os.kill(int(row['pid']), 0)
                except ProcessLookupError:
                    continue
                raise AssertionError('client left running')
        finally:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGKILL)
                p.wait()


if __name__ == '__main__':
    exercise(signal.SIGINT)
    exercise(signal.SIGTERM)
    exercise(fail_child=True)
    print('PASS: both count syntaxes, invalid counts, unique IDs, encrypted config, private logs, Ctrl+C/TERM cleanup and child-failure cleanup')
