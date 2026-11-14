import argparse
import datetime
import gzip
import io
import platform
import re
import subprocess
import tarfile
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
TARGETS = ('linux-amd64', 'linux-arm64', 'darwin-arm64')


def output(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def verify_binary(binary, target, version, commit, date):
    if b'/nix/store/' in binary.read_bytes():
        raise ValueError('Release binary contains Nix store references')

    if target.startswith('linux-'):
        headers = output('readelf', '--program-headers', '--dynamic', str(binary))
        if re.search(r'\b(INTERP|NEEDED)\b', headers):
            raise ValueError('Linux release binary must be statically linked')
    else:
        libraries = output('otool', '-L', str(binary)).splitlines()[1:]
        for library in libraries:
            name = library.strip().split(' (', 1)[0]
            if not name.startswith(('/usr/lib/', '/System/Library/Frameworks/')):
                raise ValueError(f'Non-system Darwin library: {name}')

        subprocess.run(['codesign', '--verify', '--strict', str(binary)], check=True)
        signature = subprocess.run(
            ['codesign', '--display', '--verbose=2', str(binary)],
            check=True,
            capture_output=True,
            text=True,
        )
        if 'Signature=adhoc' not in signature.stderr:
            raise ValueError('Darwin release binary must have an ad-hoc signature')

    with tempfile.TemporaryDirectory(prefix='wakatime-release-home-') as home:
        env = {'HOME': home, 'PATH': '/usr/bin:/bin', 'TMPDIR': home}
        expected = (
            f'wakatime-cli\n  Version: {version}\n  Commit: {commit}\n'
            f'  Built: {date}\n  OS/Arch: {target.replace("-", "/")}\n'
        )
        for args, value in (
            (['--version'], version + '\n'),
            (['--version', '--verbose'], expected),
        ):
            actual = subprocess.check_output([str(binary), *args], env=env, text=True)
            if actual != value:
                raise ValueError(f'Unexpected version output: {actual!r}')

        subprocess.run(
            [str(binary), '--help'], env=env, check=True, stdout=subprocess.DEVNULL
        )


def write_archive(path, binary, timestamp):
    with (
        path.open('wb') as stream,
        gzip.GzipFile(
            filename='', mode='wb', fileobj=stream, mtime=timestamp
        ) as compressed,
        tarfile.open(
            fileobj=compressed, mode='w', format=tarfile.USTAR_FORMAT
        ) as archive,
    ):
        for name, source, mode in (
            ('wakatime-cli', binary, 0o755),
            ('LICENSE', ROOT / 'LICENSE', 0o644),
        ):
            data = source.read_bytes()
            member = tarfile.TarInfo(name)
            member.size = len(data)
            member.mode = mode
            member.mtime = timestamp
            archive.addfile(member, io.BytesIO(data))


def main():
    parser = argparse.ArgumentParser(
        description='Build and verify a native portable release archive.'
    )
    parser.add_argument('target', choices=TARGETS)
    target = parser.parse_args().target

    machines = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}
    machine = machines[platform.machine()]
    native = f'{platform.system().lower()}-{machine}'
    if target != native:
        raise ValueError(f'Target {target} requires a native runner, got {native}')

    version = (ROOT / 'VERSION').read_text().strip()
    if not re.fullmatch(
        r'(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)-hakula\.([1-9]\d*)', version
    ):
        raise ValueError('VERSION must use X.Y.Z-hakula.N')

    commit = output('git', 'rev-parse', 'HEAD')
    timestamp = int(output('git', 'show', '-s', '--format=%ct', 'HEAD'))
    date = datetime.datetime.fromtimestamp(timestamp, datetime.UTC).strftime(
        '%Y-%m-%dT%H:%M:%SZ'
    )
    destination = ROOT / 'dist'
    destination.mkdir(exist_ok=True)
    archive = destination / f'wakatime-cli-{version}-{target}.tar.gz'

    with tempfile.TemporaryDirectory(prefix='wakatime-release-build-') as build:
        subprocess.run(
            [
                'make',
                f'build-{target}',
                f'VERSION={version}',
                f'COMMIT={commit}',
                f'DATE={date}',
                'CGO_ENABLED=0',
                f'BUILD_DIR={build}',
                'GOBUILD=go build -trimpath',
            ],
            cwd=ROOT,
            check=True,
        )
        binary = Path(build) / f'wakatime-cli-{target}'
        verify_binary(binary, target, version, commit, date)
        write_archive(archive, binary, timestamp)

    with tempfile.TemporaryDirectory(prefix='wakatime-release-extract-') as extracted:
        with tarfile.open(archive) as packaged:
            packaged.extractall(extracted, filter='data')
        verify_binary(Path(extracted) / 'wakatime-cli', target, version, commit, date)

    print(archive)


if __name__ == '__main__':
    main()
