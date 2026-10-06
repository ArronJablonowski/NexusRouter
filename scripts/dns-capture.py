#!/usr/bin/env python3
"""Administrator-operated conventional DNS packet capture; never launched by Web UI.

Captures system-wide TCP/UDP port 53, including harness subprocesses. This cannot
inspect encrypted DNS (DoH/DoT), cache hits, or prove process/task attribution.
"""
import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--interface', required=True, help='Explicit tcpdump interface (use tcpdump -D to list)')
    parser.add_argument('--directory', required=True, help='New absolute private capture directory')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('Administrator privileges required; no capture was started')
    if not args.interface or args.interface.startswith('-') or any(c.isspace() for c in args.interface):
        parser.error('Invalid interface')
    directory = pathlib.Path(args.directory)
    if not directory.is_absolute() or directory.exists():
        parser.error('Use a new absolute directory; existing captures are never overwritten')
    executable = shutil.which('tcpdump', path='/usr/sbin:/usr/bin:/sbin:/bin')
    if not executable:
        parser.error('System tcpdump is required')
    os.umask(0o077)
    directory.mkdir(mode=0o700, parents=False)
    record = {'version': 1, 'scope': 'system-wide DNS on selected interface',
              'interface': args.interface, 'host': os.uname().nodename,
              'coverage': 'TCP/UDP port 53 only; no encrypted DNS or task attribution',
              'started_unix': time.time(), 'state': 'starting'}
    metadata = directory / 'capture.json'
    metadata.write_text(json.dumps(record, indent=2))
    # Bound each run to 100,000 packets. Existing captures are never overwritten.
    # Keep stderr as capture/drop statistics evidence.
    command = [executable, '-n', '-U', '-i', args.interface, '-s', '0',
               '-c', '100000', '-w', str(directory / 'dns.pcap'),
               'udp port 53 or tcp port 53']
    with (directory / 'capture.stderr').open('wb') as stderr:
        process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=stderr)
        record.update(state='running', pid=process.pid)
        metadata.write_text(json.dumps(record, indent=2))
        try:
            result = process.wait()
        except KeyboardInterrupt:
            process.terminate()
            result = process.wait()
    record.update(state='stopped' if result == 0 else 'stopped_with_error',
                  exit_code=result, finished_unix=time.time())
    metadata.write_text(json.dumps(record, indent=2))
    return result


if __name__ == '__main__':
    sys.exit(main())
