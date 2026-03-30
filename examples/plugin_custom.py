#!/usr/bin/env python3
"""
Example PulseWatch plugin — outputs custom metrics as JSON.
Register in agent config:
  {
    "name": "custom",
    "command": "/path/to/this_plugin.py",
    "timeout": "5s"
  }
"""
import json
import os
import subprocess

metrics = {}

# Example: count running docker containers
try:
    result = subprocess.run(
        ['docker', 'ps', '-q'],
        capture_output=True, text=True, timeout=3
    )
    count = len([l for l in result.stdout.splitlines() if l.strip()])
    metrics['docker.running_containers'] = float(count)
except Exception:
    pass

# Example: open file descriptors for current process
try:
    fd_count = len(os.listdir('/proc/self/fd'))
    metrics['process.open_fds'] = float(fd_count)
except Exception:
    pass

# Example: check if a service is listening on a port
import socket
def port_open(host, port):
    try:
        with socket.create_connection((host, port), timeout=1):
            return 1.0
    except Exception:
        return 0.0

metrics['service.nginx']    = port_open('localhost', 80)
metrics['service.postgres'] = port_open('localhost', 5432)
metrics['service.redis']    = port_open('localhost', 6379)

print(json.dumps(metrics))
